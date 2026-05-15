// Package prompt implements `olifant prompt build "<goal>"` — decomposes a
// high-level goal into a Prompt-Step Protocol v1 plan.yaml (knowledge-base/
// dsl/psp-v1.md). The pipeline mirrors challenge: embed → retrieve → synth
// → validate → write, with auto-split when step count > psp.MaxStepsPerPlan.
package prompt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// embedGoalMaxChars caps the goal text before embedding. nomic-embed-text
// rejects inputs above ~5000 chars even with truncate=true; cap defensively.
const embedGoalMaxChars = 3500

// Hit is one retrieval result row from a single Chroma collection.
type Hit struct {
	Doc      string
	Meta     map[string]interface{}
	Distance float32
	// Scope is `<scope>/<family>` for provenance breadcrumbs in the prompt
	// (e.g., "backend/corpus", "webapp/code_history").
	Scope string
}

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
	qEmb, eerr := oc.Embed(ctx, cfg.Embedder, []string{capChars(cfg.Goal, embedGoalMaxChars)})
	if eerr != nil {
		return nil, 0, 0, fmt.Errorf("embed goal: %w", eerr)
	}
	if len(qEmb) != 1 {
		return nil, 0, 0, fmt.Errorf("embed returned %d vectors, expected 1", len(qEmb))
	}
	embedMs = time.Since(embedStart).Milliseconds()

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = allScopes
	}

	retrStart := time.Now()
	for _, scope := range scopes {
		for _, family := range collFamilies {
			if family != "corpus" && !codeScopes[scope] {
				continue
			}
			collName := family + "_" + strings.ReplaceAll(scope, "-", "_")
			coll, cerr := cc.EnsureCollection(ctx, collName, nil)
			if cerr != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: collection unavailable (%v) — skipping\n", collName, cerr)
				}
				continue
			}
			res, qerr := cc.Query(ctx, coll.ID, chroma.QueryRequest{
				QueryEmbeddings: qEmb,
				NResults:        cfg.TopN,
			})
			if qerr != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: query failed (%v) — skipping\n", collName, qerr)
				}
				continue
			}
			if len(res.Documents) == 0 {
				continue
			}
			for i := range res.Documents[0] {
				hits = append(hits, Hit{
					Doc:      res.Documents[0][i],
					Meta:     res.Metadatas[0][i],
					Distance: res.Distances[0][i],
					Scope:    scope + "/" + family,
				})
			}
		}
	}
	retrieveMs = time.Since(retrStart).Milliseconds()

	sortHitsByDistance(hits)
	if len(hits) > cfg.TopN {
		hits = hits[:cfg.TopN]
	}

	if cfg.Verbose {
		fmt.Println("  retrieved hits:")
		for i, h := range hits {
			fmt.Printf("    %2d  d=%.4f  [%s]  %v\n", i+1, h.Distance, h.Scope, h.Meta["source"])
		}
	}
	return hits, embedMs, retrieveMs, nil
}

// sortHitsByDistance sorts in place — lower distance is more relevant.
func sortHitsByDistance(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
}

// capChars trims s at a UTF-8 boundary so the result is ≤ maxChars bytes.
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
