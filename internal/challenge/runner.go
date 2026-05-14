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
		cfg.TopN = 8
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.1
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

	// 2. Retrieve from each scope
	retrStart := time.Now()
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"}
	}
	var hits []retrievedHit
	for _, scope := range scopes {
		collName := "corpus_" + strings.ReplaceAll(scope, "-", "_")
		coll, err := cc.EnsureCollection(ctx, collName, nil)
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  scope %s: collection unavailable (%v) — skipping\n", scope, err)
			}
			continue
		}
		res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{
			QueryEmbeddings: qEmb,
			NResults:        cfg.TopN,
		})
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  scope %s: query failed (%v) — skipping\n", scope, err)
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
				Scope:    scope,
			})
		}
	}
	retrieveMs := time.Since(retrStart).Milliseconds()

	// Sort by distance (lower = closer) and keep top globalTopN across scopes
	sort.Slice(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
	globalTopN := cfg.TopN * 2
	if len(hits) > globalTopN {
		hits = hits[:globalTopN]
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

// systemPrompt sets the model's role + output contract.
const systemPrompt = `You are Olifant — a controlled-language domain expert for the ElatusDev/AkademiaPlus SaaS platform.

Your job in this turn is the CHALLENGE step: read the user's request and produce a structured verdict in YAML.

Hard rules:
- Cite ONLY artifact IDs that appear verbatim in the RETRIEVED CONTEXT (e.g., D17, AP3, SB-04, WA-L-01). Never invent IDs.
- If the retrieved context does not cover the request, set verdict: OUT_OF_SCOPE and explain in clarify[].
- Output VALID YAML only. No surrounding prose. No code fences.

Output schema (produce these top-level keys exactly):

challenge:
  request: "<verbatim user ask>"
  verdict: VALID | VALID_WITH_CAVEATS | INVALID | NEEDS_CLARIFICATION | OUT_OF_SCOPE
  confirms:
    - claim: "<what the request aligns with>"
      cites: [<artifact_id or source#anchor>, ...]
  contradicts:
    - claim: "<what the request is at odds with>"
      counter: "<the rule/decision that makes it problematic>"
      cites: [<artifact_id or source#anchor>, ...]
  clarify:
    - question: "<what to ask the user>"
      why_asking: "<why ambiguity matters>"
  applicable_rules:
    standards: [<rule_ids>]
    patterns: [<pattern_names>]
    anti_patterns_to_avoid: [<AP_ids>]
    decisions_to_honor: [<D_ids>]
  proceed: confirm_with_user | abort | proceed_directly

Verdict semantics:
- VALID — request aligns with platform; proceed_directly.
- VALID_WITH_CAVEATS — proceed, but flag the caveats; confirm_with_user.
- INVALID — request contradicts hard rules; abort.
- NEEDS_CLARIFICATION — ambiguous; ask in clarify[]; confirm_with_user.
- OUT_OF_SCOPE — corpus doesn't cover this; abort.

Be concise. Empty list members are omitted (don't pad).`
