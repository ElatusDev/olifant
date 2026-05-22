package challenge

import (
	"context"
	"fmt"
	"strings"

	"github.com/ElatusDev/olifant/internal/chroma"
)

// allScopes is the v1 default scope list used when no case-level scope filter
// was supplied. Mirrors the prior in-line literal so both retrieval paths
// share the convention.
var allScopes = []string{
	"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process",
}

// unionWithUniversal returns the scope filter list always including
// "universal" so cross-cutting corpus + failure_modes entries surface
// regardless of the case's stack. An empty input expands to the full
// scope list.
func unionWithUniversal(scopes []string) []string {
	if len(scopes) == 0 {
		out := make([]string, len(allScopes))
		copy(out, allScopes)
		return out
	}
	for _, s := range scopes {
		if s == "universal" {
			out := make([]string, len(scopes))
			copy(out, scopes)
			return out
		}
	}
	out := make([]string, 0, len(scopes)+1)
	out = append(out, "universal")
	out = append(out, scopes...)
	return out
}

// retrieveV1 is the legacy retrieval path: 5 collection families ×
// N scopes, top-K per (family, scope). Returned hits carry Scope =
// "<scope>/<family>" for disambiguation. Errors per-collection are
// logged + skipped (so a missing family doesn't fail the whole run).
func retrieveV1(
	ctx context.Context, cc *chroma.Client, qEmb [][]float32,
	scopes []string, topN int, verbose bool,
) []retrievedHit {
	collFamilies := []string{"corpus", "code", "history", "code_history", "failure_modes"}
	codeScopes := map[string]bool{
		"backend": true, "webapp": true, "mobile": true, "e2e": true, "infra": true,
	}
	var hits []retrievedHit
	for _, scope := range scopes {
		for _, family := range collFamilies {
			if family != "corpus" && family != "failure_modes" && !codeScopes[scope] {
				continue
			}
			collName := family + "_" + strings.ReplaceAll(scope, "-", "_")
			coll, err := cc.EnsureCollection(ctx, collName, nil)
			if err != nil {
				if verbose {
					fmt.Printf("  %s: collection unavailable (%v) — skipping\n", collName, err)
				}
				continue
			}
			res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{
				QueryEmbeddings: qEmb,
				NResults:        topN,
			})
			if err != nil {
				if verbose {
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
					Scope:    scope + "/" + family,
				})
			}
		}
	}
	return hits
}

// retrieveV2 is the RAG-pivot Phase A2 retrieval path: one tag-indexed
// collection (olifant-v2-curriculum), where-filtered by scope ∈ scopes.
// We over-request (topN × ~3, capped at 30) so the downstream sort +
// fm-reserve selector still has cross-scope diversity to pick from.
func retrieveV2(
	ctx context.Context, cc *chroma.Client, qEmb [][]float32,
	collection string, scopes []string, topN int, verbose bool,
) ([]retrievedHit, error) {
	coll, err := cc.EnsureCollection(ctx, collection, nil)
	if err != nil {
		return nil, fmt.Errorf("ensure %s: %w", collection, err)
	}
	where := buildV2ScopeWhere(scopes)
	nReq := topN * 3
	if nReq < 15 {
		nReq = 15
	}
	if nReq > 30 {
		nReq = 30
	}
	res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{
		QueryEmbeddings: qEmb,
		NResults:        nReq,
		Where:           where,
	})
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", collection, err)
	}
	if len(res.Documents) == 0 {
		if verbose {
			fmt.Printf("  v2 %s: 0 documents returned\n", collection)
		}
		return nil, nil
	}
	hits := make([]retrievedHit, 0, len(res.Documents[0]))
	for i := range res.Documents[0] {
		meta := res.Metadatas[0][i]
		scope, _ := meta["scope"].(string)
		kind, _ := meta["item_kind"].(string)
		hits = append(hits, retrievedHit{
			Doc:      res.Documents[0][i],
			Meta:     meta,
			Distance: res.Distances[0][i],
			Scope:    scope + "/" + kind,
		})
	}
	if verbose {
		fmt.Printf("  v2 %s: %d hits (where=%v)\n", collection, len(hits), where)
	}
	return hits, nil
}

// buildV2ScopeWhere assembles the Chroma `where` clause for v2 retrieval.
// Empty scopes -> no filter (returns nil). Single scope -> equality match
// (avoids the $in operator). Multi-scope -> $in.
func buildV2ScopeWhere(scopes []string) map[string]interface{} {
	if len(scopes) == 0 {
		return nil
	}
	if len(scopes) == 1 {
		return map[string]interface{}{"scope": scopes[0]}
	}
	return map[string]interface{}{
		"scope": map[string]interface{}{"$in": scopes},
	}
}
