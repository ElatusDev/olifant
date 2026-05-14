// Package challenge implements Step 0 of the olifant pipeline: parse a user
// request, retrieve relevant corpus chunks via ChromaDB, ask the synthesizer
// model to produce a verdict in CNL/YAML, and emit it to stdout.
package challenge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// Config drives one challenge run.
type Config struct {
	Request        string
	OllamaURL      string
	ChromaURL      string
	Embedder       string
	Synthesizer    string
	Tenant         string
	Database       string
	Scopes         []string // collections to query; empty = all
	TopN           int      // chunks per scope (default 8)
	Temperature    float64  // 0.1 default; 0 = deterministic
	MaxTokens      int      // synthesizer num_predict (default 1024)
	Verbose        bool
}

// Result is the final emitted artifact.
type Result struct {
	RequestText    string
	RetrievedCount int
	YAMLOutput     string
	Elapsed        time.Duration
	EmbedMs        int64
	RetrieveMs     int64
	SynthMs        int64
	SynthEvalCount int
	SynthTokensSec float64
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

	// 1. Embed the request
	embedStart := time.Now()
	qEmb, err := oc.Embed(ctx, cfg.Embedder, []string{cfg.Request})
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	if len(qEmb) != 1 {
		return nil, fmt.Errorf("embed returned %d vectors, expected 1", len(qEmb))
	}
	embedMs := time.Since(embedStart).Milliseconds()

	// 2. Retrieve from each scope, querying BOTH the docs collection
	//    (corpus_<scope>) and the code collection (code_<scope>).
	retrStart := time.Now()
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"}
	}
	// Universal scope has no code analogue (we never ingest "universal code").
	// platform-process likewise.
	collFamilies := []string{"corpus", "code"}
	codeScopes := map[string]bool{
		"backend": true, "webapp": true, "mobile": true, "e2e": true, "infra": true,
	}
	var hits []retrievedHit
	for _, scope := range scopes {
		for _, family := range collFamilies {
			if family == "code" && !codeScopes[scope] {
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

	// 4. Synthesize
	synthStart := time.Now()
	resp, err := oc.Generate(ctx, ollama.GenerateRequest{
		Model:  cfg.Synthesizer,
		System: systemPrompt,
		Prompt: prompt,
		Options: map[string]interface{}{
			"temperature": cfg.Temperature,
			"num_predict": cfg.MaxTokens,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	synthMs := time.Since(synthStart).Milliseconds()

	return &Result{
		RequestText:    cfg.Request,
		RetrievedCount: len(hits),
		YAMLOutput:     strings.TrimSpace(resp.Response),
		Elapsed:        time.Since(start),
		EmbedMs:        embedMs,
		RetrieveMs:     retrieveMs,
		SynthMs:        synthMs,
		SynthEvalCount: resp.EvalCount,
		SynthTokensSec: resp.TokensPerSec(),
	}, nil
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

Your job in this turn is the CHALLENGE step: read the user's request and produce a structured YAML verdict.

BE SKEPTICAL BY DEFAULT. Your job is to FIND problems before they ship. False approvals are far more harmful than over-flagging. When in doubt, demand clarification or mark OUT_OF_SCOPE.

HARD RULES:
1. Cite ONLY artifact IDs (D###, AP##, SB-##, SI-##, AMS-##, WA-..., TBU-##, etc.) that appear VERBATIM in the RETRIEVED CONTEXT. Never invent IDs or recall them from training.
2. The verdict MUST be exactly one of: VALID | VALID_WITH_CAVEATS | INVALID | NEEDS_CLARIFICATION | OUT_OF_SCOPE. No other words. Do not use "inconclusive", "PASSED", "accepted", "success".
3. Output VALID YAML only. No surrounding prose. No code fences. No markdown headers.
4. Top-level key MUST be "challenge:". Field structure MUST match the examples exactly. Do not invent fields like "challengeVerdict", "isAccepted", "feedback", "pointsAwarded", or "rationale".
5. Use OUT_OF_SCOPE when the retrieved context does not address the request's actual topic — even if the request itself looks well-formed.

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
