// Claude backend for the synth seam — shells out to the `claude` CLI
// (subscription-authenticated, no API key), mirroring the invocation and
// parsing pattern of internal/psp/claude_code_executor.go as a separate
// type (workflow olifant-cloud-synth-v1, D-F4-6). Schema enforcement is
// server-side via --json-schema.
//
// Known seam mismatches, accepted by the workflow:
//   - Request.Temperature is ignored — the CLI exposes no temperature
//     control (IA1; gate GF4 compensates with a double-run flip check).
//   - Request.MaxTokens is ignored — no CLI flag; output length is
//     bounded by the schema and the model's own stop behavior.
package synth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Claude runs synthesis through the claude CLI subprocess.
type Claude struct {
	binary  string
	effort  string
	timeout time.Duration
}

// NewClaude returns the claude-CLI synthesizer backend. The binary path
// must already be validated (config.ResolveClaude guarantees this when
// ok=true). The model is taken per-request from Request.Model.
func NewClaude(binary, effort string, timeout time.Duration) *Claude {
	return &Claude{binary: binary, effort: effort, timeout: timeout}
}

// claudeJSONResult mirrors `claude -p --output-format json`. With
// --json-schema the parsed object lands in structured_output and result
// is empty; without, result holds free-form text.
type claudeJSONResult struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	StopReason       string          `json:"stop_reason"`
	Usage            struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Generate invokes the CLI once and returns the raw (schema-conformant)
// JSON text. EvalCount maps to output tokens; EvalDuration to wall-clock.
func (c *Claude) Generate(ctx context.Context, req Request) (*Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	args := []string{
		"-p", req.Prompt,
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if c.effort != "" {
		args = append(args, "--effort", c.effort)
	}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if req.Schema != nil {
		schemaBytes, err := json.Marshal(req.Schema)
		if err != nil {
			return nil, fmt.Errorf("claude synth: marshal schema: %w", err)
		}
		args = append(args, "--json-schema", string(schemaBytes))
	}

	cmd := exec.CommandContext(ctx, c.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if runErr != nil {
		return nil, fmt.Errorf("claude synth subprocess: %w (stderr: %s)", runErr, trimErr(stderr.String()))
	}

	var resp claudeJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("claude synth: unparseable output: %w (stdout=%q)", err, capStr(stdout.String(), 200))
	}
	if resp.IsError {
		return nil, fmt.Errorf("claude synth returned error (subtype=%s): %s", resp.Subtype, resp.Result)
	}

	var raw string
	switch {
	case len(resp.StructuredOutput) > 0:
		raw = string(resp.StructuredOutput)
	default:
		raw = stripFence(strings.TrimSpace(resp.Result))
	}
	if req.Schema != nil && raw == "" {
		return nil, fmt.Errorf("claude synth: empty response when schema was provided (subtype=%s, stop=%s)",
			resp.Subtype, resp.StopReason)
	}

	return &Response{
		Text:         raw,
		EvalCount:    resp.Usage.OutputTokens,
		EvalDuration: elapsed.Nanoseconds(),
	}, nil
}

// stripFence removes a leading ```json fence + trailing ``` if the model
// wraps JSON despite --json-schema. Defensive.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// trimErr caps stderr for error messages — the CLI is chatty on auth and
// rate-limit failures.
func trimErr(s string) string {
	return capStr(strings.TrimSpace(s), 400)
}

func capStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
