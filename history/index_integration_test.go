package history

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

func historyIndexServers(t *testing.T, upserts *int) (ollamaURL, chromaURL string) {
	t.Helper()
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.5.0"}`))
		case "/api/embed":
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(oll.Close)

	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_, _ = w.Write([]byte(`{"nanosecond heartbeat":1}`))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"c1","name":"history"}`))
		case strings.HasSuffix(r.URL.Path, "/upsert"):
			if upserts != nil {
				var req struct {
					IDs []string `json:"ids"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				*upserts += len(req.IDs)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

func TestIndex_FullPath(t *testing.T) {
	repo := makeHistoryRepo(t)
	records, _, err := Walk(context.Background(), repo, "core-api", "backend", "", ScanConfig{})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no records walked")
	}

	upserts := 0
	oURL, cURL := historyIndexServers(t, &upserts)
	stats, err := Index(context.Background(), records, IndexConfig{
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "bge-m3",
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.CommitChunks == 0 {
		t.Error("expected commit chunks")
	}
	if stats.CommitUpserted+stats.SnapshotUpserted == 0 {
		t.Error("expected upserts")
	}
	if upserts == 0 {
		t.Error("chroma saw no upsert ids")
	}
}

func TestIndex_DryRun(t *testing.T) {
	repo := makeHistoryRepo(t)
	records, _, _ := Walk(context.Background(), repo, "core-api", "backend", "", ScanConfig{})
	stats, err := Index(context.Background(), records, IndexConfig{DryRun: true})
	if err != nil {
		t.Fatalf("Index dry-run: %v", err)
	}
	if stats.CommitChunks == 0 {
		t.Error("dry-run should still count chunks")
	}
	if stats.CommitUpserted != 0 {
		t.Errorf("dry-run must not upsert, got %d", stats.CommitUpserted)
	}
}

func TestIndex_OllamaUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	repo := makeHistoryRepo(t)
	records, _, _ := Walk(context.Background(), repo, "core-api", "backend", "", ScanConfig{})
	_, err := Index(context.Background(), records, IndexConfig{OllamaURL: dead.URL, ChromaURL: dead.URL})
	if err == nil || !strings.Contains(err.Error(), "ollama unreachable") {
		t.Errorf("want ollama-unreachable error, got %v", err)
	}
}

func TestChunkMetadataForChroma(t *testing.T) {
	c := corpus.Chunk{
		Source: "core-api@abc#commit", Scope: "backend", DocType: "commit-summary",
		SourceSHA: "abc", Title: "feat: x",
		Metadata: corpus.ChunkMetadata{
			Section:       "sec",
			CitesOutbound: []string{"D1"},
			TechTags:      []string{"java"},
		},
	}
	m := chunkMetadataForChroma(c)
	if m["source"] != "core-api@abc#commit" || m["scope"] != "backend" {
		t.Errorf("base fields = %v", m)
	}
	if m["cites_outbound"] != "D1" || m["tech_tags"] != "java" {
		t.Errorf("joined fields = %v", m)
	}
	bare := chunkMetadataForChroma(corpus.Chunk{Source: "x", Scope: "y", DocType: "z"})
	if _, ok := bare["tech_tags"]; ok {
		t.Error("empty tech_tags should be omitted")
	}
}
