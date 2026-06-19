//go:build integration

package chroma_test

import (
	"context"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/livetest"
)

func TestLive_Heartbeat(t *testing.T) {
	rt := livetest.RequireChroma(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hb, err := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase).Heartbeat(ctx)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb <= 0 {
		t.Errorf("heartbeat = %d, want positive", hb)
	}
}

// TestLive_CollectionRoundtrip exercises the full write path against the live
// ChromaDB: ensure tenant/db/collection, upsert vectors, count, query nearest.
// Uses a fixed test collection with idempotent ids so re-runs don't grow it.
func TestLive_CollectionRoundtrip(t *testing.T) {
	rt := livetest.RequireChroma(t)
	cc := livetest.Chroma(t, rt)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coll, err := cc.EnsureCollection(ctx, "olifant_itest", map[string]interface{}{
		"hnsw:space":   "cosine",
		"olifant_kind": "integration-test",
	})
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if coll.ID == "" {
		t.Fatal("collection has empty id")
	}

	req := chroma.UpsertRequest{
		IDs:        []string{"itest-a", "itest-b", "itest-c"},
		Embeddings: [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}},
		Documents:  []string{"alpha doc", "beta doc", "gamma doc"},
		Metadatas: []map[string]interface{}{
			{"label": "a"}, {"label": "b"}, {"label": "c"},
		},
	}
	if err := cc.Upsert(ctx, coll.ID, req); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := cc.Count(ctx, coll.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n < 3 {
		t.Errorf("count = %d, want >= 3", n)
	}

	// Query nearest to the 'alpha' vector → should rank itest-a first.
	res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{
		QueryEmbeddings: [][]float32{{0.9, 0.1, 0, 0}},
		NResults:        3,
		Include:         []string{"documents", "metadatas", "distances"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.IDs) != 1 || len(res.IDs[0]) == 0 {
		t.Fatalf("query returned no hits: %+v", res.IDs)
	}
	if res.IDs[0][0] != "itest-a" {
		t.Errorf("nearest = %q, want itest-a (alpha vector)", res.IDs[0][0])
	}
	if len(res.Distances) == 0 || len(res.Distances[0]) == 0 {
		t.Error("expected distances in query response")
	}
	t.Logf("collection=%s id=%s count=%d nearest=%s", coll.Name, coll.ID, n, res.IDs[0][0])
}

func TestLive_EnsureIdempotent(t *testing.T) {
	rt := livetest.RequireChroma(t)
	cc := livetest.Chroma(t, rt)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// EnsureTenant/Database are no-ops when already present (covered by the
	// Chroma helper); EnsureCollection with get_or_create returns the same id.
	a, err := cc.EnsureCollection(ctx, "olifant_itest", nil)
	if err != nil {
		t.Fatalf("EnsureCollection a: %v", err)
	}
	b, err := cc.EnsureCollection(ctx, "olifant_itest", nil)
	if err != nil {
		t.Fatalf("EnsureCollection b: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("get_or_create not idempotent: %s != %s", a.ID, b.ID)
	}
}
