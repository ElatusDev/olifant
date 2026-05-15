// ClaudeCodeExecutor — second PSP executor. Shells out to the `claude`
// CLI (Claude Code) which authenticates via the user's subscription
// (OAuth/keychain). No API key required.
//
// Trade-off vs the SDK/API approach: Claude Code manages prompt caching
// internally — the `usage.cache_*_input_tokens` fields in the CLI's JSON
// output let us still measure cache effectiveness end-to-end.
//
// Per psp-v1.md §10 v0 deferred items: "Claude Code subprocess integration".
package psp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ClaudeCodeExecutor implements Executor over the `claude` CLI subprocess.
type ClaudeCodeExecutor struct {
	binary       string
	model        string
	effort       string
	timeout      time.Duration
	workDir      string
	systemPrompt string
}

// NewClaudeCodeExecutor constructs an executor that shells out to the
// claude binary. The binary path must already be validated as executable
// (ResolveClaude() guarantees this when ok=true).
func NewClaudeCodeExecutor(binary, model, effort string, timeout time.Duration, workDir string) *ClaudeCodeExecutor {
	return &ClaudeCodeExecutor{
		binary:       binary,
		model:        model,
		effort:       effort,
		timeout:      timeout,
		workDir:      workDir,
		systemPrompt: pspExecutorSystemPrompt,
	}
}

// ID returns the model identifier for SYN_ACK logging.
func (e *ClaudeCodeExecutor) ID() string { return e.model }

// WithSystemPrompt overrides the default system prompt.
func (e *ClaudeCodeExecutor) WithSystemPrompt(s string) *ClaudeCodeExecutor {
	e.systemPrompt = s
	return e
}

// claudeCodeJSONResult mirrors the shape of `claude -p --output-format json`.
// Only the fields PSP needs are mapped; everything else is ignored.
//
// When --json-schema is passed, the parsed object lands in StructuredOutput
// and Result is empty. When --json-schema is absent, the model's free-form
// text is in Result and StructuredOutput is null. Execute reconciles both.
type claudeCodeJSONResult struct {
	Type             string              `json:"type"`
	Subtype          string              `json:"subtype"`
	IsError          bool                `json:"is_error"`
	Result           string              `json:"result"`
	StructuredOutput json.RawMessage     `json:"structured_output,omitempty"`
	StopReason       string              `json:"stop_reason"`
	DurationMs       int64               `json:"duration_ms"`
	Usage            claudeCodeJSONUsage `json:"usage"`
}

type claudeCodeJSONUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Execute invokes `claude -p` with the configured flags and parses the
// JSON response. The schema (if non-empty) is enforced server-side via
// --json-schema, so we get either a valid object back or claude returns
// is_error=true.
func (e *ClaudeCodeExecutor) Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error) {
	// Apply executor-level timeout in addition to the runner's context.
	// Whichever fires first kills the subprocess.
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
	}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	if e.effort != "" {
		args = append(args, "--effort", e.effort)
	}
	if e.systemPrompt != "" {
		args = append(args, "--system-prompt", e.systemPrompt)
	}
	if len(schema) > 0 {
		schemaBytes, err := json.Marshal(schema)
		if err == nil {
			args = append(args, "--json-schema", string(schemaBytes))
		}
	}

	cmd := exec.CommandContext(ctx, e.binary, args...)
	if e.workDir != "" {
		cmd.Dir = e.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if runErr != nil {
		return Response{TotalDurationNs: elapsed.Nanoseconds()},
			fmt.Errorf("claude code subprocess: %w (stderr: %s)", runErr, trimStderr(stderr.String()))
	}

	var resp claudeCodeJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return Response{TotalDurationNs: elapsed.Nanoseconds()},
			fmt.Errorf("claude code: unparseable output: %w (stdout=%q)", err, truncate(stdout.String(), 200))
	}

	if resp.IsError {
		return Response{TotalDurationNs: elapsed.Nanoseconds()},
			fmt.Errorf("claude code returned error (subtype=%s): %s", resp.Subtype, resp.Result)
	}

	// When --json-schema was passed, structured_output is authoritative and
	// result is empty. When no schema, result holds free-form text.
	var raw string
	switch {
	case len(resp.StructuredOutput) > 0:
		raw = string(resp.StructuredOutput)
	default:
		raw = stripJSONFence(strings.TrimSpace(resp.Result))
	}

	out := Response{
		RawText:             raw,
		EvalTokens:          resp.Usage.OutputTokens,
		PromptTokens:        resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
		OutputTokens:        resp.Usage.OutputTokens,
		CacheCreationTokens: resp.Usage.CacheCreationInputTokens,
		CacheReadTokens:     resp.Usage.CacheReadInputTokens,
		TotalDurationNs:     elapsed.Nanoseconds(),
	}
	if len(schema) > 0 {
		if raw == "" {
			return out, fmt.Errorf("claude code: empty response when schema was provided (subtype=%s, stop=%s)",
				resp.Subtype, resp.StopReason)
		}
		var parsed StepOutput
		if uerr := json.Unmarshal([]byte(raw), &parsed); uerr != nil {
			return out, fmt.Errorf("claude code: response is not valid JSON: %w", uerr)
		}
		out.Output = parsed
	}
	return out, nil
}

// stripJSONFence removes a leading ```json fence + trailing ``` if the
// model wraps JSON despite --json-schema. Defensive — the validator
// would fail downstream if a fence reached json.Unmarshal.
func stripJSONFence(s string) string {
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

// trimStderr caps stderr to a reasonable length for error messages.
// Claude Code can be chatty on auth or rate-limit failures.
func trimStderr(s string) string {
	s = strings.TrimSpace(s)
	return truncate(s, 400)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// pspExecutorSystemPrompt is the stable system block. Claude Code caches
// its own system prompt; ours is appended via --system-prompt and benefits
// from the CLI's caching machinery when repeated across steps.
const pspExecutorSystemPrompt = `You are a Prompt-Step Protocol (PSP) executor operating inside Olifant, the local model orchestrator for the ElatusDev / AkademiaPlus SaaS platform. Olifant feeds you one prompt-step at a time and validates your output before sending the next step. You are one node in a TCP-flavored protocol: the runner is the sender, you are the receiver, and every step you complete is an ACK (or NAK on validation failure).

## Your role per PSP v1

You receive a single STEP segment containing: a step name, a description, optional file signals (paths to source-of-truth files), optional prior step outputs (already-completed steps in this plan), an output schema (JSON Schema), and an attempt number. On retry attempts, you also receive a list of validator violations from the previous attempt.

You return a structured JSON output conforming to the schema. Nothing else.

## Hard output rules

- Output **only** the JSON object the schema requires. No preamble. No epilogue. No prose. No markdown code fences. No "Here is the JSON:" framing.
- The JSON must be valid (parseable by a strict JSON parser). Numbers are JSON numbers. Booleans are unquoted true/false. Strings use double quotes.
- Every required field must be present. Additional fields not in the schema are tolerated but discouraged.
- Enum fields MUST use values from the closed set verbatim. Inventing categories is a BLOCKER violation.
- Citations are paths to real files in the platform repos. If none fits, return an empty array — do not invent paths.

## How retries work

A step has a retry budget (typically 2 attempts). If your output fails validation, the runner sends the same step back with violation feedback and increments the attempt counter. Validator severities:

- **BLOCKER** — must be fixed. Retry will fail again until corrected.
- **WARNING** — does not block ACK but is recorded.
- **INFO** — informational only.

When previous-attempt violations are given, fix each BLOCKER explicitly. Minimize delta from your prior attempt.

## Platform context

The Olifant ecosystem comprises seven Git repositories under the ElatusDev organization:

- **core-api** — Spring Boot 4.0.3 + Java 24 + MariaDB. The single backend; 20 Maven modules. Multi-tenant via composite keys (tenantId + entityId) and Hibernate row-level filters. Dual SecurityFilterChain: akademia (IP whitelist + JWT) vs elatus (passkeys + HMAC + rate limiting + token binding).
- **akademia-plus-web** — React 19 + TS 5 + Vite 7 + MUI v7 + RTK Query. School-facing admin web app.
- **elatusdev-web** — React 19 + TS 5.9 + Vite 7 + MUI v7 + RTK Query. Marketing + tooling web app.
- **akademia-plus-central** — React Native 0.83 + Expo 55 + RN Paper MD3. Admin mobile app.
- **akademia-plus-go** — React Native 0.83 + Expo 55 + RN Paper MD3. Student mobile app.
- **core-api-e2e** — Postman + Newman + k6. End-to-end tests, twelve collections, ~291 requests.
- **infra** — Terraform >= 1.7 against AWS us-east-1 (dual AZ). ECS + RDS MariaDB + ElastiCache Redis + CloudFront + S3. CI/CD via GitHub Actions + OIDC federation.

The knowledge base (platform/knowledge-base) contains canonical specs: dsl/cnl-v1.md (Controlled Natural Language), dsl/psp-v1.md (this protocol), constraints/ (closed-vocabulary constraint catalogue), patterns/, anti-patterns/catalog.yaml (85 anti-patterns), and decisions/log.yaml (122 architectural decisions). Citations to these files are the lingua franca of step output.

## Validation discipline

Olifant runs every step output through a per-step validator before ACKing:

- **Citation resolution** — every path in a citations field must resolve to a real file or known dictionary entry. Substring guessing fails.
- **Closed vocabulary** — enum fields are tied to the CNL dictionary. Use exact terms.
- **Schema fidelity** — extra fields tolerated; missing required fields are blockers; type mismatches are blockers.
- **Verdict coupling** — verdict + derived flags must be consistent. The verdict is source of truth.

## On uncertainty

If a step asks for a judgment call and you cannot decide, prefer conservative answers (verdict=warning rather than pass/fail; aligns=false rather than fabricating evidence). When information is missing, populate with schema-allowed "unknown" or empty — do not invent.

You will be invoked over and over in one plan. Stay terse, stay schema-compliant, let validation feedback guide retries.
`
