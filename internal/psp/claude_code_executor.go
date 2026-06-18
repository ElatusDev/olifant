// ClaudeCodeExecutor — second PSP executor. Drives the `claude` CLI (Claude
// Code), which authenticates via the user's subscription (OAuth/keychain).
// No API key required.
//
// The CLI invocation + result parsing live in internal/claudecli, shared with
// the synth backend; this type is the thin PSP-side adapter (consolidated in
// arch-consolidation-v1, F1). Claude Code manages prompt caching internally —
// the usage.cache_*_input_tokens fields let us still measure cache
// effectiveness end-to-end via Response.Cache*Tokens.
//
// Per psp-v1.md §10 v0 deferred items: "Claude Code subprocess integration".
package psp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ElatusDev/olifant/internal/claudecli"
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

// Execute invokes `claude -p` via claudecli and maps the result onto a PSP
// Response. The schema (if non-empty) is enforced server-side via
// --json-schema; claudecli returns an error on an empty response, and this
// adapter additionally unmarshals the raw JSON into the typed StepOutput.
func (e *ClaudeCodeExecutor) Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error) {
	res, err := claudecli.Run(ctx, e.binary, claudecli.Args{
		Prompt: prompt,
		Model:  e.model,
		Effort: e.effort,
		System: e.systemPrompt,
		Schema: schema,
	}, claudecli.Options{WorkDir: e.workDir, Timeout: e.timeout})
	if err != nil {
		return Response{TotalDurationNs: res.ElapsedNs}, err
	}

	out := Response{
		RawText:             res.Raw,
		EvalTokens:          res.Usage.OutputTokens,
		PromptTokens:        res.Usage.InputTokens + res.Usage.CacheCreationInputTokens + res.Usage.CacheReadInputTokens,
		OutputTokens:        res.Usage.OutputTokens,
		CacheCreationTokens: res.Usage.CacheCreationInputTokens,
		CacheReadTokens:     res.Usage.CacheReadInputTokens,
		TotalDurationNs:     res.ElapsedNs,
	}
	if len(schema) > 0 {
		var parsed StepOutput
		if uerr := json.Unmarshal([]byte(res.Raw), &parsed); uerr != nil {
			return out, fmt.Errorf("claude code: response is not valid JSON: %w", uerr)
		}
		out.Output = parsed
	}
	return out, nil
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
