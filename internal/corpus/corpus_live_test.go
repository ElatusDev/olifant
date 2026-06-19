//go:build integration

package corpus_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/retrieval"
)

// TestLive_IndexRoundtrip indexes a tiny NDJSON corpus into an isolated
// `corpus_itest` collection on the live stack (real embed + upsert), then
// counts and retrieves it back. Uses a synthetic `itest` scope so it never
// touches the real corpus_<scope> collections.
func TestLive_IndexRoundtrip(t *testing.T) {
	rt := livetest.RequireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	corpusDir := t.TempDir()
	lines := `{"chunk_id":"itest-c1","source":"itest/a.md","scope":"itest","doc_type":"doc","body":"Tenant scoping is enforced by a PRE_INSERT listener that stamps tenant_id."}
{"chunk_id":"itest-c2","source":"itest/b.md","scope":"itest","doc_type":"doc","body":"Composite primary keys use @IdClass with (tenantId, entityId)."}
`
	if err := os.WriteFile(filepath.Join(corpusDir, "itest.ndjson"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := corpus.Index(ctx, corpus.IndexConfig{
		CorpusDir:  corpusDir,
		OllamaURL:  rt.OllamaURL,
		ChromaURL:  rt.ChromaURL,
		Embedder:   rt.Embedder,
		Tenant:     rt.ChromaTenant,
		Database:   rt.ChromaDatabase,
		OnlyScopes: []string{"itest"},
	})
	if err != nil {
		t.Fatalf("corpus.Index: %v", err)
	}
	if stats.ChunksUpserted < 2 {
		t.Errorf("ChunksUpserted = %d, want >= 2", stats.ChunksUpserted)
	}

	// Verify via the live collection: count + a real retrieval query.
	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
	coll, err := cc.EnsureCollection(ctx, "corpus_itest", nil)
	if err != nil {
		t.Fatalf("EnsureCollection corpus_itest: %v", err)
	}
	n, err := cc.Count(ctx, coll.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n < 2 {
		t.Errorf("corpus_itest count = %d, want >= 2", n)
	}

	qEmb, err := retrieval.Embed(ctx, ollama.New(rt.OllamaURL), rt.Embedder,
		"how is tenant id stamped on insert", retrieval.DefaultEmbedMaxChars)
	if err != nil {
		t.Fatalf("Embed query: %v", err)
	}
	res, err := cc.Query(ctx, coll.ID, chroma.QueryRequest{QueryEmbeddings: qEmb, NResults: 2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.IDs) == 0 || len(res.IDs[0]) == 0 {
		t.Fatal("no hits from corpus_itest")
	}
	t.Logf("indexed %d chunks; corpus_itest count=%d; top hit=%s", stats.ChunksUpserted, n, res.IDs[0][0])
}
