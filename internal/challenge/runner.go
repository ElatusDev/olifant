// Package challenge implements Step 0 of the olifant pipeline: parse a user
// request, retrieve relevant corpus chunks via ChromaDB, ask the synthesizer
// model to produce a verdict in CNL/YAML, and emit it to stdout.
package challenge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
	"gopkg.in/yaml.v3"
)

// embedRequestMaxChars caps the request before it's sent to the embedder.
// nomic-embed-text via Ollama rejects inputs above its 2048-token default
// context regardless of the `truncate` flag, so we cap at 2000 chars to
// keep the worst-case token count under the ceiling. The synthesizer
// still sees the full request in its prompt.
const embedRequestMaxChars = 2000

// Config drives one challenge run.
type Config struct {
	Request          string
	OllamaURL        string
	ChromaURL        string
	Embedder         string
	Synthesizer      string
	Tenant           string
	Database         string
	Scopes           []string // collections to query; empty = all
	TopN             int      // chunks per scope (default 8)
	Temperature      float64  // 0.1 default; 0 = deterministic
	MaxTokens        int      // synthesizer num_predict (default 1024)
	Verbose          bool
	Validator        *CiteValidator // optional; nil disables cite validation
	MaxValidateRetries int          // default 1 retry on cite hallucination
}

// Result is the final emitted artifact.
type Result struct {
	RequestText             string
	RetrievedCount          int
	RetrievedSources        []string // top-N source paths fed to the synthesizer
	RawJSON                 string   // the model's literal JSON output
	YAMLOutput              string
	JSONValid               bool     // true if the synth output parsed as JSON
	Elapsed                 time.Duration
	EmbedMs                 int64
	RetrieveMs              int64
	SynthMs                 int64
	SynthEvalCount          int
	SynthTokensSec          float64
	CiteAttempts            int         // 1 = first try clean; 2+ = retried after violations
	RemainingCiteViolations []Violation // unresolved after final attempt (empty = clean)
}

// Run executes the full pipeline.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	start := time.Now()
	if cfg.TopN <= 0 {
		cfg.TopN = 6
	}
	// Temperature 0 is allowed and meaningful (greedy decoding).
	// Only patch in default if user explicitly passes a negative sentinel.
	if cfg.Temperature < 0 {
		cfg.Temperature = 0
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

	// 1. Embed the request (capped — long --file inputs would otherwise
	//    exceed the embedder context window).
	embedStart := time.Now()
	qEmb, err := oc.Embed(ctx, cfg.Embedder, []string{capChars(cfg.Request, embedRequestMaxChars)})
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	if len(qEmb) != 1 {
		return nil, fmt.Errorf("embed returned %d vectors, expected 1", len(qEmb))
	}
	embedMs := time.Since(embedStart).Milliseconds()

	// 2. Retrieve from each scope, querying four collection families:
	//      corpus_<scope>        — KB docs (specs, dictionaries, retros)
	//      code_<scope>          — current code state (from `repo ingest`)
	//      history_<scope>       — commit summaries (from `history index`)
	//      code_history_<scope>  — file content at past commits
	retrStart := time.Now()
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"}
	}
	// universal + platform-process have neither code nor history analogues —
	// we never ingest code or commit history for those scopes.
	collFamilies := []string{"corpus", "code", "history", "code_history"}
	codeScopes := map[string]bool{
		"backend": true, "webapp": true, "mobile": true, "e2e": true, "infra": true,
	}
	var hits []retrievedHit
	for _, scope := range scopes {
		for _, family := range collFamilies {
			// code, history, and code_history are all stack-scoped — skip them
			// for universal + platform-process.
			if family != "corpus" && !codeScopes[scope] {
				continue
			}
			collName := family + "_" + strings.ReplaceAll(scope, "-", "_")
			coll, err := cc.EnsureCollection(ctx, collName, nil)
			if err != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: collection unavailable (%v) — skipping\n", collName, err)
				}
				continue
			}
			res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{
				QueryEmbeddings: qEmb,
				NResults:        cfg.TopN,
			})
			if err != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: query failed (%v) — skipping\n", collName, err)
				}
				continue
			}
			if len(res.Documents) == 0 {
				continue
			}
			for i := range res.Documents[0] {
				hits = append(hits, retrievedHit{
					Doc:      res.Documents[0][i],
					Meta:     res.Metadatas[0][i],
					Distance: res.Distances[0][i],
					Scope:    scope + "/" + family, // disambiguate source family
				})
			}
		}
	}
	retrieveMs := time.Since(retrStart).Milliseconds()

	// Sort by distance (lower = closer) and keep TopN globally — not per-scope.
	// Tighter than topN*2: dilution by off-topic chunks was a major failure mode.
	sort.Slice(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
	if len(hits) > cfg.TopN {
		hits = hits[:cfg.TopN]
	}

	if cfg.Verbose {
		fmt.Println("  retrieved hits:")
		for i, h := range hits {
			fmt.Printf("    %2d  d=%.4f  [%s]  %v\n", i+1, h.Distance, h.Scope, h.Meta["source"])
		}
	}

	// 3. Build prompt
	prompt := buildChallengePrompt(cfg.Request, hits)

	// 4. Synthesize — Ollama's `format` field is set to a JSON Schema so the
	//    model is grammar-constrained to emit only schema-conformant output.
	//    If a CiteValidator is wired, validate every cite resolves to a real
	//    dictionary term or a real file path; on violation, retry once with
	//    the violations surfaced in the prompt.
	maxRetries := cfg.MaxValidateRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if cfg.Validator != nil && maxRetries == 0 {
		maxRetries = 1
	}

	// Build a per-run schema with enum constraints derived from the
	// validator's per-scope dictionary + concept terms. The grammar engine
	// rejects generic categories like "use_of_hooks" / "consistent_tagging"
	// at decode time, forcing the model to choose from our explicit list
	// or emit an empty array.
	scopeForSchema := scopes
	dynamicSchema := BuildChallengeSchema(cfg.Validator, scopeForSchema)

	gen := func(promptText string) (*ollama.GenerateResponse, error) {
		return oc.Generate(ctx, ollama.GenerateRequest{
			Model:  cfg.Synthesizer,
			System: systemPrompt,
			Prompt: promptText,
			Format: dynamicSchema,
			Options: map[string]interface{}{
				"temperature": cfg.Temperature,
				"num_predict": cfg.MaxTokens,
			},
		})
	}

	synthStart := time.Now()
	resp, err := gen(prompt)
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	attempts := 1
	totalEvalCount := resp.EvalCount
	totalEvalDuration := resp.EvalDuration
	var lastViolations []Violation
	if cfg.Validator != nil {
		violations, vErr := cfg.Validator.Validate(resp.Response)
		if vErr != nil && cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  validator parse error: %v\n", vErr)
		}
		lastViolations = violations
		for HasBlockers(violations) && attempts <= maxRetries {
			blockers := FilterBlockers(violations)
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  validator retry #%d: %d BLOCKER + %d non-blocker\n",
					attempts, len(blockers), len(violations)-len(blockers))
				for _, v := range blockers {
					fmt.Fprintf(os.Stderr, "    [%s] %s @ %s", v.Code, v.Note, v.Location)
					if v.Value != "" {
						fmt.Fprintf(os.Stderr, "  (%q)", v.Value)
					}
					fmt.Fprintln(os.Stderr)
				}
			}
			retryPrompt := prompt + cfg.Validator.RetryPromptAddendum(violations, cfg.Request, scopes)
			retryResp, rerr := gen(retryPrompt)
			if rerr != nil {
				if cfg.Verbose {
					fmt.Fprintf(os.Stderr, "  retry generate failed: %v — keeping previous output\n", rerr)
				}
				break
			}
			attempts++
			totalEvalCount += retryResp.EvalCount
			totalEvalDuration += retryResp.EvalDuration
			resp = retryResp
			violations, _ = cfg.Validator.Validate(resp.Response)
			lastViolations = violations
			if !HasBlockers(violations) {
				break
			}
		}
	}
	synthMs := time.Since(synthStart).Milliseconds()

	// Convert JSON output to YAML for display continuity.
	yamlOut, jsonValid := jsonToYAML(resp.Response)

	// Build a synthetic GenerateResponse-like view for tokens/sec across all attempts.
	tokensPerSec := 0.0
	if totalEvalDuration > 0 && totalEvalCount > 0 {
		tokensPerSec = float64(totalEvalCount) / (float64(totalEvalDuration) / 1e9)
	}

	// Collect source paths from retrieved hits for the short-term record.
	sourcePaths := make([]string, 0, len(hits))
	for _, h := range hits {
		if src, ok := h.Meta["source"].(string); ok && src != "" {
			sourcePaths = append(sourcePaths, src)
		}
	}

	return &Result{
		RequestText:             cfg.Request,
		RetrievedCount:          len(hits),
		RetrievedSources:        sourcePaths,
		RawJSON:                 strings.TrimSpace(resp.Response),
		YAMLOutput:              yamlOut,
		JSONValid:               jsonValid,
		Elapsed:                 time.Since(start),
		EmbedMs:                 embedMs,
		RetrieveMs:              retrieveMs,
		SynthMs:                 synthMs,
		SynthEvalCount:          totalEvalCount,
		SynthTokensSec:          tokensPerSec,
		CiteAttempts:            attempts,
		RemainingCiteViolations: lastViolations,
	}, nil
}

// ExtractVerdict returns (verdict, proceed) parsed from RawJSON, empty
// strings if parsing fails. Used by the short-term writer to index turns.
func (r *Result) ExtractVerdict() (verdict, proceed string) {
	var shape struct {
		Challenge struct {
			Verdict string `json:"verdict"`
			Proceed string `json:"proceed"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal([]byte(r.RawJSON), &shape); err != nil {
		return "", ""
	}
	return shape.Challenge.Verdict, shape.Challenge.Proceed
}

// jsonToYAML parses model output as JSON and re-marshals as YAML. Returns
// (originalString, false) if parsing fails — useful for debugging.
func jsonToYAML(raw string) (string, bool) {
	var data interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return strings.TrimSpace(raw), false
	}
	out, err := yaml.Marshal(data)
	if err != nil {
		return strings.TrimSpace(raw), false
	}
	return strings.TrimRight(string(out), "\n"), true
}

// capChars trims s to maxChars at a UTF-8 boundary.
func capChars(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	end := maxChars
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[:end]
}

// retrievedHit is one Chroma result row, package-shared so Run() and
// buildChallengePrompt() can pass slices freely.
type retrievedHit struct {
	Doc      string
	Meta     map[string]interface{}
	Distance float32
	Scope    string
}

func buildChallengePrompt(request string, hits []retrievedHit) string {
	var sb strings.Builder
	sb.WriteString("USER REQUEST:\n")
	sb.WriteString(strings.TrimSpace(request))
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
	sb.WriteString("\nPRODUCE THE CHALLENGE VERDICT NOW, AS YAML, FOLLOWING THE SCHEMA EXACTLY.\n")
	sb.WriteString("Output YAML only — no surrounding prose, no code fences.\n")
	return sb.String()
}

// systemPrompt is the strict olifant-challenge contract. Embedded few-shot
// examples are critical — retrieved chunks often contain audit/eval-result
// templates that the model otherwise imitates, drifting away from our schema.
const systemPrompt = `You are Olifant — a controlled-language domain expert for the ElatusDev/AkademiaPlus SaaS platform.

Your job in this turn is the CHALLENGE step: read the user's request and produce a structured verdict (output is schema-constrained JSON, rendered to the user as YAML).

BE RIGOROUS, NOT REFLEXIVE. Demand specific evidence for problems — but do NOT manufacture problems where none exist in the retrieved context. Generic concerns ("uses dependency injection", "follows naming conventions", "could use better error handling", "common security best practices") are NOT problems. They are not citations.

HARD RULES:
1. EVERY value in cites[] AND every entry in applicable_rules.{standards,patterns,anti_patterns_to_avoid,decisions_to_honor} MUST appear verbatim in the RETRIEVED CONTEXT — either as an artifact ID (D###, AP##, SB-##, SI-##, AMS-##, WA-..., TBU-##, ABS-##, …) or as a literal source path (e.g., core-api/.../Foo.java#L1-L80, decisions/log.yaml#D17). NEVER invent generic categories like "magic_strings", "hardcoded_secrets", "owasp_top10", "nist_800_53", "consistent_code_style", "single_responsibility_principle", "dependency_injection". If you cannot point to a retrieved chunk that names the rule, leave the slot empty.

2. INVALID requires CONCRETE EVIDENCE. You must identify a specific rule (anti-pattern ID, decision ID, standard rule ID) from the retrieved context that the request demonstrably violates, and a contradicts[] entry must cite it. Without that, the verdict is NOT INVALID — choose VALID_WITH_CAVEATS, NEEDS_CLARIFICATION, or OUT_OF_SCOPE instead. False INVALIDs are as harmful as false approvals.

3. OUT_OF_SCOPE when the retrieved context does not address the request's actual topic — even if the request itself looks well-formed.

4. The 'request' field MUST be the user's verbatim request, or a faithful one-sentence summary for very long inputs (code files). Do NOT put placeholders like "clarification_required" or "no_changes_required" in the 'request' field.

VERDICT SEMANTICS:
- VALID — aligns with platform rules; proceed: proceed_directly.
- VALID_WITH_CAVEATS — proceeds but with notable caveats; proceed: confirm_with_user.
- INVALID — contradicts hard rules or anti-patterns; proceed: abort.
- NEEDS_CLARIFICATION — ambiguous request; ask questions in clarify[]; proceed: confirm_with_user.
- OUT_OF_SCOPE — corpus does not cover this topic; proceed: abort.

EXAMPLE 1 — INVALID (clear policy violation):

User asks: "Use AsyncStorage to persist Firebase ID tokens for offline auth"
Retrieved context contains mobile secure-storage rules.

challenge:
  request: "Use AsyncStorage to persist Firebase ID tokens for offline auth"
  verdict: INVALID
  confirms: []
  contradicts:
    - claim: "Persisting auth tokens in AsyncStorage"
      counter: "AsyncStorage is not encrypted at rest; auth material requires Keychain (iOS) / Keystore (Android)"
      cites: [AMS-02]
  clarify: []
  applicable_rules:
    standards: [AMS-02]
    patterns: []
    anti_patterns_to_avoid: [AMS-02]
    decisions_to_honor: []
  proceed: abort

EXAMPLE 2 — OUT_OF_SCOPE (corpus does not cover the topic):

User asks: "What is the best Python library for web scraping?"
Retrieved context contains only unrelated platform docs.

challenge:
  request: "What is the best Python library for web scraping?"
  verdict: OUT_OF_SCOPE
  confirms: []
  contradicts: []
  clarify:
    - question: "Is this related to a specific ElatusDev/AkademiaPlus task?"
      why_asking: "The platform corpus does not cover general Python web scraping"
  applicable_rules:
    standards: []
    patterns: []
    anti_patterns_to_avoid: []
    decisions_to_honor: []
  proceed: abort

EXAMPLE 3 — VALID_WITH_CAVEATS (aligned but with risks worth flagging):

User asks: "Add a new TenantScoped entity for invoices with composite key (tenantId, invoiceId)"
Retrieved context contains tenant-isolation rules and the composite-key pattern.

challenge:
  request: "Add a new TenantScoped entity for invoices with composite key (tenantId, invoiceId)"
  verdict: VALID_WITH_CAVEATS
  confirms:
    - claim: "Composite key with tenantId enforces row-level isolation"
      cites: [AP3]
  contradicts: []
  clarify:
    - question: "Does the entity require @SQLDelete with tenantId in the WHERE clause for soft delete?"
      why_asking: "Soft-delete must preserve tenant isolation per AP3"
  applicable_rules:
    standards: []
    patterns: ["TenantScoped"]
    anti_patterns_to_avoid: [AP3]
    decisions_to_honor: []
  proceed: confirm_with_user

NOW PRODUCE THE VERDICT FOR THE REAL USER REQUEST USING THE EXACT SAME STRUCTURE. Always include all 5 fields (confirms, contradicts, clarify, applicable_rules, proceed) — use empty lists [] when nothing applies. Output the YAML and nothing else.`
