// Package prompt implements `olifant prompt build "<goal>"` — decomposes a
// high-level goal into a Prompt-Step Protocol v1 plan.yaml (knowledge-base/
// dsl/psp-v1.md). The pipeline mirrors challenge: embed → retrieve → synth
// → validate → write, with auto-split when step count > psp.MaxStepsPerPlan.
package prompt

import (
	"context"
	"fmt"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/retrieval"
)

// Hit is one retrieval result row (shared shape — see internal/retrieval).
type Hit = retrieval.Hit

// retrieveConfig is the subset of Config needed by retrieve().
type retrieveConfig struct {
	Goal      string
	OllamaURL string
	ChromaURL string
	Embedder  string
	Tenant    string
	Database  string
	Scopes    []string
	TopN      int
	Verbose   bool
}

// allScopes is the default scope set when none is specified.
var allScopes = []string{
	"universal", "backend", "webapp", "mobile",
	"e2e", "infra", "platform-process",
}

// codeScopes are scopes that have code_/history_/code_history_ collection
// families alongside corpus_. universal + platform-process have only corpus.
var codeScopes = map[string]bool{
	"backend": true, "webapp": true, "mobile": true,
	"e2e": true, "infra": true,
}

// collFamilies are the Chroma collection name prefixes queried per scope.
// corpus is always queried; the code families only for codeScopes.
var collFamilies = []string{"corpus", "code", "history", "code_history"}

// retrieve embeds the goal once, queries every relevant Chroma collection,
// and returns a globally top-N sorted slice of hits.
func retrieve(ctx context.Context, cfg retrieveConfig) (hits []Hit, embedMs, retrieveMs int64, err error) {
	if cfg.TopN <= 0 {
		cfg.TopN = 8
	}
	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

	embedStart := time.Now()
	qEmb, eerr := retrieval.Embed(ctx, oc, cfg.Embedder, cfg.Goal, retrieval.DefaultEmbedMaxChars)
	if eerr != nil {
		return nil, 0, 0, fmt.Errorf("embed goal: %w", eerr)
	}
	embedMs = time.Since(embedStart).Milliseconds()

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = allScopes
	}

	retrStart := time.Now()
	hits = retrieval.QueryScopedFamilies(ctx, cc, qEmb, retrieval.FamilyConfig{
		Families:       collFamilies,
		AlwaysFamilies: map[string]bool{"corpus": true},
		CodeScopes:     codeScopes,
		Scopes:         scopes,
		TopN:           cfg.TopN,
		Verbose:        cfg.Verbose,
	})
	retrieveMs = time.Since(retrStart).Milliseconds()

	hits = retrieval.SortByDistanceTruncate(hits, cfg.TopN)

	if cfg.Verbose {
		fmt.Println("  retrieved hits:")
		for i, h := range hits {
			fmt.Printf("    %2d  d=%.4f  [%s]  %v\n", i+1, h.Distance, h.Scope, h.Meta["source"])
		}
	}
	return hits, embedMs, retrieveMs, nil
}

// sourcePathsFromHits returns unique "source" metadata values in hit order.
// Used by the short-term ledger writer to record which docs seeded the plan.
func sourcePathsFromHits(hits []Hit) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		src, _ := h.Meta["source"].(string)
		if src == "" || seen[src] {
			continue
		}
		seen[src] = true
		out = append(out, src)
	}
	return out
}
