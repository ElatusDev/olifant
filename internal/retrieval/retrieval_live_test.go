//go:build integration

package retrieval_test

import (
	"context"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/retrieval"
)

// TestLive_EmbedAndQueryCorpus embeds a real query and retrieves from the live
// corpus_<scope> collections, exercising the embed + scoped-family query seam
// against the actual indexed corpus.
func TestLive_EmbedAndQueryCorpus(t *testing.T) {
	rt := livetest.RequireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	oc := ollama.New(rt.OllamaURL)
	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)

	qEmb, err := retrieval.Embed(ctx, oc, rt.Embedder,
		"how is tenant scoping enforced on entities", retrieval.DefaultEmbedMaxChars)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	hits := retrieval.QueryScopedFamilies(ctx, cc, qEmb, retrieval.FamilyConfig{
		Families:       []string{"corpus"},
		AlwaysFamilies: map[string]bool{"corpus": true},
		CodeScopes:     map[string]bool{"backend": true},
		Scopes:         []string{"backend", "universal"},
		TopN:           5,
	})
	if len(hits) == 0 {
		t.Skip("no corpus hits — corpus may not be indexed in this ChromaDB (run `olifant corpus index`)")
	}

	top := retrieval.SortByDistanceTruncate(hits, 5)
	if len(top) == 0 || len(top) > 5 {
		t.Fatalf("SortByDistanceTruncate returned %d hits, want 1..5", len(top))
	}
	// Sorted ascending by distance.
	for i := 1; i < len(top); i++ {
		if top[i-1].Distance > top[i].Distance {
			t.Errorf("hits not sorted by distance: %f > %f", top[i-1].Distance, top[i].Distance)
		}
	}
	// Each hit carries a scope + a non-empty doc.
	for i, h := range top {
		if h.Scope == "" || h.Doc == "" {
			t.Errorf("hit %d malformed: scope=%q docLen=%d", i, h.Scope, len(h.Doc))
		}
	}
	t.Logf("retrieved %d hits; top distance=%.4f scope=%s source=%v",
		len(top), top[0].Distance, top[0].Scope, top[0].Meta["source"])
}
