// Package retrieval is the shared embed→query→rank core for olifant's RAG
// paths. prompt, challenge, and validate each used to carry their own copy of
// the per-collection query loop, the UTF-8-safe input cap, and the
// distance sort; this package owns them once (arch-consolidation-v1, F2).
//
// Callers keep what is genuinely caller-specific: the collection set they
// query (via FamilyConfig), and how they render hits into a prompt. The
// vector-store boundary remains internal/chroma; embedding remains
// internal/ollama — this package orchestrates them, it does not replace them.
package retrieval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// DefaultEmbedMaxChars caps embed input for the goal/claim paths (prompt,
// validate). bge-m3 / nomic-embed-text via Ollama reject inputs above ~5000
// chars even with truncate=true; cap defensively. The challenge path uses a
// tighter cap (it embeds the raw request), so Embed takes maxChars explicitly
// rather than assuming one value.
const DefaultEmbedMaxChars = 3500

// Hit is one retrieval result row from a single Chroma collection. Scope is
// "<scope>/<family>" for provenance breadcrumbs in the prompt
// (e.g., "backend/corpus", "webapp/code_history").
type Hit struct {
	Doc      string
	Meta     map[string]interface{}
	Distance float32
	Scope    string
}

// FamilyConfig parameterises a scoped-families query. A (family, scope) pair
// is queried when the family is in AlwaysFamilies OR the scope is in
// CodeScopes — i.e. corpus-style families are queried for every scope, while
// code/history families are queried only for scopes that have code ingested.
type FamilyConfig struct {
	Families       []string
	AlwaysFamilies map[string]bool
	CodeScopes     map[string]bool
	Scopes         []string
	TopN           int
	Verbose        bool
}

// Embed embeds a single text (capped at DefaultEmbedMaxChars) and returns the
// one-vector result. A non-1 vector count is an error — there's nothing to
// query against otherwise.
func Embed(ctx context.Context, oc *ollama.Client, model, text string, maxChars int) ([][]float32, error) {
	qEmb, err := oc.Embed(ctx, model, []string{CapChars(text, maxChars)})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(qEmb) != 1 {
		return nil, fmt.Errorf("embed returned %d vectors, expected 1", len(qEmb))
	}
	return qEmb, nil
}

// QueryScopedFamilies queries every (family, scope) pair selected by cfg and
// returns the collected hits (unsorted — callers that want a global top-N
// apply SortByDistanceTruncate). Per-collection errors (missing collection,
// transport, query) are logged when Verbose and skipped, so a missing family
// doesn't fail the whole run.
func QueryScopedFamilies(ctx context.Context, cc *chroma.Client, qEmb [][]float32, cfg FamilyConfig) []Hit {
	var hits []Hit
	for _, scope := range cfg.Scopes {
		for _, family := range cfg.Families {
			if !cfg.AlwaysFamilies[family] && !cfg.CodeScopes[scope] {
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
	return hits
}

// SortByDistanceTruncate sorts hits ascending by distance (lower = more
// relevant) in place and truncates to topN. topN <= 0 leaves length unchanged.
func SortByDistanceTruncate(hits []Hit, topN int) []Hit {
	sort.Slice(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
	if topN > 0 && len(hits) > topN {
		hits = hits[:topN]
	}
	return hits
}

// CapChars trims s at a UTF-8 boundary so the result is ≤ maxChars bytes.
func CapChars(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	end := maxChars
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[:end]
}
