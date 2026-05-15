package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// getEnvDebugPath returns the OLIFANT_PROMPT_DEBUG path if set. When non-
// empty, the synth request body is dumped there for offline replay.
func getEnvDebugPath() string { return os.Getenv("OLIFANT_PROMPT_DEBUG") }

// dumpRequest writes the synth request body as pretty JSON to path. Errors
// are swallowed since this is a debug aid.
func dumpRequest(path string, req ollama.GenerateRequest) {
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, body, 0o644)
}

// synthConfig is the subset of Config needed by synthesize().
type synthConfig struct {
	OllamaURL   string
	Synthesizer string
	Temperature float64
	MaxTokens   int
}

// synthResult captures the raw model output plus timing.
type synthResult struct {
	RawJSON      string
	EvalCount    int
	EvalDuration int64
	ElapsedMs    int64
}

// We deliberately do NOT override num_ctx for synthesis. Forcing a larger
// context window made the 16 GB olifant mini OOM during model reload
// (KV_CACHE_TYPE=q8_0 in the LaunchAgent isn't honoured by the homebrew
// Ollama process currently running). Stay at Ollama's loaded default
// (4096) and keep the prompt + output budget inside it. If goals need
// more room, raise it via the OLIFANT_OLLAMA env or operator config — not
// from inside this package.

// synthesize asks the synthesizer model to produce a JSON plan from the
// goal + retrieved hits. The model is grammar-constrained by planSynthSchema.
func synthesize(ctx context.Context, cfg synthConfig, goal string, hits []Hit) (*synthResult, error) {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.Temperature < 0 {
		cfg.Temperature = 0
	}
	oc := ollama.New(cfg.OllamaURL)

	prompt := buildPromptText(goal, hits)
	req := ollama.GenerateRequest{
		Model:  cfg.Synthesizer,
		System: systemPrompt,
		Prompt: prompt,
		Format: planSynthSchema(),
		Options: map[string]interface{}{
			"temperature": cfg.Temperature,
			"num_predict": cfg.MaxTokens,
		},
	}
	if dbg := getEnvDebugPath(); dbg != "" {
		dumpRequest(dbg, req)
	}
	start := time.Now()
	resp, err := oc.Generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	return &synthResult{
		RawJSON:      strings.TrimSpace(resp.Response),
		EvalCount:    resp.EvalCount,
		EvalDuration: resp.EvalDuration,
		ElapsedMs:    time.Since(start).Milliseconds(),
	}, nil
}

// buildPromptText assembles the user-facing prompt body. The goal comes
// first; retrieved chunks follow with provenance breadcrumbs so the model
// can ground signals[] entries against real source paths.
func buildPromptText(goal string, hits []Hit) string {
	var sb strings.Builder
	sb.WriteString("USER GOAL:\n")
	sb.WriteString(strings.TrimSpace(goal))
	sb.WriteString("\n\nRETRIEVED CONTEXT (top corpus chunks ordered by similarity):\n\n")
	for i, h := range hits {
		source, _ := h.Meta["source"].(string)
		anchor, _ := h.Meta["source_anchor"].(string)
		aid, _ := h.Meta["artifact_id"].(string)
		fmt.Fprintf(&sb, "--- chunk %d (distance=%.4f, scope=%s", i+1, h.Distance, h.Scope)
		if aid != "" {
			fmt.Fprintf(&sb, ", artifact_id=%s", aid)
		}
		if anchor != "" {
			fmt.Fprintf(&sb, ", anchor=%s", anchor)
		} else if source != "" {
			fmt.Fprintf(&sb, ", source=%s", source)
		}
		sb.WriteString(") ---\n")
		sb.WriteString(h.Doc)
		if !strings.HasSuffix(h.Doc, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\nPRODUCE THE PROMPT-STEP PLAN NOW AS JSON, FOLLOWING THE SCHEMA EXACTLY.\n")
	sb.WriteString("Output JSON only — no surrounding prose, no code fences.\n")
	return sb.String()
}

// planSynthSchema is the JSON Schema passed to Ollama's `format` field. The
// model is constrained at decode time to emit schema-conformant output.
//
// Schema intentionally avoids: `pattern`, `minLength`, `maxLength`, `enum`,
// `minItems`, `maxItems`. Empirically these constraints crash the Ollama
// grammar engine ("model runner has unexpectedly stopped") on nested
// schemas with qwen2.5:14b-q6_K at default num_ctx. We keep the structural
// shape only; semantic checks (step_NN id format, scope membership, step
// count cap) are enforced in Go via validateLogicalPlan and the post-synth
// transform.
func planSynthSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"required":             []string{"plan"},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"plan": map[string]interface{}{
				"type":                 "object",
				"required":             []string{"goal", "steps"},
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"goal":  map[string]interface{}{"type": "string"},
					"scope": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"steps": map[string]interface{}{
						"type":  "array",
						"items": stepSynthSchema(),
					},
				},
			},
		},
	}
}

func stepSynthSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"required":             []string{"id", "name", "description", "expected_output"},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"id":          map[string]interface{}{"type": "string"},
			"name":        map[string]interface{}{"type": "string"},
			"description": map[string]interface{}{"type": "string"},
			"signals":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"depends_on":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"expected_output": map[string]interface{}{
				"type":                 "object",
				"required":             []string{"type"},
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"type":   map[string]interface{}{"type": "string"},
					"fields": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
		},
	}
}

// systemPrompt instructs the synthesizer to decompose the goal into PSP
// prompt-steps. The shape is grammar-constrained via planSynthSchema; this
// system message drives the semantic content of each step.
const systemPrompt = `You are Olifant — the prompt-builder role for the ElatusDev/AkademiaPlus SaaS platform.

Your job in this turn is to decompose the USER GOAL into a Prompt-Step Protocol (PSP) v1 plan that can be executed step-by-step by an executor (local model or Claude). Output must be schema-conformant JSON.

HARD RULES:

1. Each step is a single, validatable unit of work — small enough that one executor turn can complete it, large enough that it produces a tangible artifact.

2. Step IDs are sequential and zero-padded: step_01, step_02, step_03, … . IDs are unique within the plan. depends_on values MUST reference an earlier step id — no forward references, no cycles. A step with no preconditions has depends_on: [].

3. signals[] entries SHOULD be drawn from the RETRIEVED CONTEXT — use the verbatim source path from a retrieved chunk (e.g., "core-api/multi-tenant-data/src/main/java/.../NewsFeedItemDataModel.java" or "knowledge-base/standards/SECURITY-QUALITY-STANDARD.md"). Do not invent file paths.

4. description is a CONCRETE instruction: what to read, what to write, what to verify. Use platform vocabulary (TenantScoped, @SQLDelete, composite key, Hibernate filters, …) when grounded by retrieved context.

5. expected_output.type defaults to "object". fields[] lists the keys the executor should emit in its structured response (e.g., ["java_source", "rationale"]). Keep fields atomic — one purpose per field.

6. Prefer ≤ 25 steps. Larger plans are auto-split into sequential sub-plans by the builder, but a tight plan is always better than a sprawling one. Sequence steps so the work flows: survey → design → implement → test → verify → document — pick the shape that matches the goal; not every plan needs all five.

7. plan.goal is the user's verbatim goal (or a faithful one-sentence summary for very long inputs). plan.scope picks from: universal, backend, webapp, mobile, e2e, infra, platform-process — at most one or two.

PLAN SHAPE (the schema your output must satisfy):

{
  "plan": {
    "goal": "<verbatim user goal>",
    "scope": ["backend"],
    "steps": [
      {
        "id": "step_01",
        "name": "<≤120 char title>",
        "description": "<full executor-facing instruction>",
        "signals": ["<file path from retrieved chunk>"],
        "depends_on": [],
        "expected_output": {
          "type": "object",
          "fields": ["summary", "rationale"]
        }
      }
    ]
  }
}

EXAMPLE — goal: "Add a TenantScoped entity for invoices":

{
  "plan": {
    "goal": "Add a TenantScoped entity for invoices",
    "scope": ["backend"],
    "steps": [
      {
        "id": "step_01",
        "name": "Survey existing TenantScoped exemplars",
        "description": "Read core-api/multi-tenant-data/src/main/java/com/akademiaplus/newsfeed/NewsFeedItemDataModel.java. Identify the composite-key pattern, @SQLDelete annotation with tenantId in WHERE clause, and the @Component @Scope(prototype) wiring. Emit a 6-line summary of the pattern's invariants.",
        "signals": ["core-api/multi-tenant-data/src/main/java/com/akademiaplus/newsfeed/NewsFeedItemDataModel.java"],
        "depends_on": [],
        "expected_output": { "type": "object", "fields": ["pattern_summary", "invariants"] }
      },
      {
        "id": "step_02",
        "name": "Design the InvoiceDataModel composite key",
        "description": "Define InvoiceDataModelId as @IdClass with fields (tenantId, invoiceId) per AP3. Emit the Java class header signature and field declarations.",
        "signals": ["knowledge-base/anti-patterns/catalog.md#AP3"],
        "depends_on": ["step_01"],
        "expected_output": { "type": "object", "fields": ["id_class_source"] }
      }
    ]
  }
}

NOW PRODUCE THE PLAN FOR THE REAL USER GOAL USING THE EXACT SAME STRUCTURE. Output the JSON and nothing else.`
