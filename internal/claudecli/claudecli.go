// Package claudecli is the single boundary to the `claude` CLI subprocess
// (subscription-authenticated, no API key). Both the synth backend
// (internal/synth) and the PSP executor (internal/psp) drive the CLI through
// this package, so the invocation contract — flags, JSON-result shape,
// structured-output reconciliation, defensive fence-stripping — lives in one
// place. The CLI's model/flag contract has changed under us before (AP104:
// claude-fable-5 retired overnight → 404), so a single drift point matters.
//
// Schema enforcement is server-side via --json-schema: when a schema is
// passed the parsed object lands in structured_output and result is empty;
// without a schema, result holds free-form text. Run reconciles both into
// Result.Raw.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Args are the inputs to one `claude -p` invocation. Optional fields
// (Model, Effort, System, Schema) are omitted from the argv when zero.
type Args struct {
	Prompt string
	Model  string
	Effort string
	System string
	Schema map[string]interface{}
}

// Options tune Run beyond the argv. WorkDir sets the subprocess CWD (the PSP
// executor uses it; the synth backend leaves it empty). Timeout, when > 0,
// bounds the call and kills the subprocess on expiry.
type Options struct {
	WorkDir string
	Timeout time.Duration
}

// Usage mirrors the CLI's usage block. It is the superset of what both
// callers need: synth reads OutputTokens only; the PSP executor reports the
// cache fields for its plan-wide cache-hit-rate line.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// Result is the reconciled output of one invocation. Raw is the
// schema-conformant JSON text (structured_output when a schema was passed,
// else the fence-stripped result). Callers apply their own typed parsing.
type Result struct {
	Raw        string
	Subtype    string
	StopReason string
	Usage      Usage
	ElapsedNs  int64
}

// jsonResult mirrors `claude -p --output-format json`. Only the fields the
// callers need are mapped; everything else is ignored.
type jsonResult struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	StopReason       string          `json:"stop_reason"`
	Usage            struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// Build returns the argv for one invocation (excluding the binary itself).
// The leading flags are always present; Model/Effort/System/Schema are
// appended only when set. Returns an error only if the schema fails to
// marshal.
func Build(a Args) ([]string, error) {
	args := []string{
		"-p", a.Prompt,
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Effort != "" {
		args = append(args, "--effort", a.Effort)
	}
	if a.System != "" {
		args = append(args, "--system-prompt", a.System)
	}
	if len(a.Schema) > 0 {
		schemaBytes, err := json.Marshal(a.Schema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema: %w", err)
		}
		args = append(args, "--json-schema", string(schemaBytes))
	}
	return args, nil
}

// Run invokes the claude CLI once and returns the reconciled Result. A
// non-empty Args.Schema makes an empty response an error (the schema was
// requested but nothing came back). On any error the returned Result still
// carries ElapsedNs (and, where parsed, Usage) so callers can record metrics.
func Run(ctx context.Context, binary string, a Args, opts Options) (Result, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	args, err := Build(a)
	if err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if runErr != nil {
		return Result{ElapsedNs: elapsed.Nanoseconds()},
			fmt.Errorf("claude subprocess: %w (stderr: %s)", runErr, capStr(strings.TrimSpace(stderr.String()), 400))
	}

	var resp jsonResult
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return Result{ElapsedNs: elapsed.Nanoseconds()},
			fmt.Errorf("claude: unparseable output: %w (stdout=%q)", err, capStr(stdout.String(), 200))
	}

	res := Result{
		Subtype:    resp.Subtype,
		StopReason: resp.StopReason,
		ElapsedNs:  elapsed.Nanoseconds(),
		Usage: Usage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		},
	}

	if resp.IsError {
		return res, fmt.Errorf("claude returned error (subtype=%s): %s", resp.Subtype, resp.Result)
	}

	switch {
	case len(resp.StructuredOutput) > 0:
		res.Raw = string(resp.StructuredOutput)
	default:
		res.Raw = stripFence(strings.TrimSpace(resp.Result))
	}

	if len(a.Schema) > 0 && res.Raw == "" {
		return res, fmt.Errorf("claude: empty response when schema was provided (subtype=%s, stop=%s)",
			resp.Subtype, resp.StopReason)
	}

	return res, nil
}

// stripFence removes a leading ```json (or bare ```) fence and trailing ```
// if the model wraps JSON despite --json-schema. Defensive — a stray fence
// would fail downstream json.Unmarshal.
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

// capStr caps s to n bytes for error messages — the CLI is chatty on auth
// and rate-limit failures.
func capStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
