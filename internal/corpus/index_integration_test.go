package corpus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

func corpusIndexServers(t *testing.T, upserts *int) (oURL, cURL string) {
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
			_, _ = w.Write([]byte(`{"id":"c1","name":"corpus"}`))
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

func writeCorpusNDJSON(t *testing.T, dir, scope string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(`{"chunk_id":"` + scope + "-" + string(rune('a'+i)) + `","source":"x","scope":"` + scope + `","doc_type":"code","body":"some body text"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, scope+".ndjson"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndex_FullPath(t *testing.T) {
	corpusDir := t.TempDir()
	writeCorpusNDJSON(t, corpusDir, "backend", 3)
	writeCorpusNDJSON(t, corpusDir, "universal", 2)

	upserts := 0
	oURL, cURL := corpusIndexServers(t, &upserts)
	stats, err := Index(context.Background(), IndexConfig{
		CorpusDir: corpusDir,
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "bge-m3",
		BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.ChunksUpserted != 5 {
		t.Errorf("ChunksUpserted = %d, want 5", stats.ChunksUpserted)
	}
	if stats.ScopesProcessed != 2 {
		t.Errorf("ScopesProcessed = %d, want 2", stats.ScopesProcessed)
	}
	if upserts != 5 {
		t.Errorf("chroma saw %d ids, want 5", upserts)
	}
}

func TestIndex_DryRun(t *testing.T) {
	corpusDir := t.TempDir()
	writeCorpusNDJSON(t, corpusDir, "backend", 3)
	oURL, cURL := corpusIndexServers(t, nil)
	stats, err := Index(context.Background(), IndexConfig{
		CorpusDir: corpusDir, OllamaURL: oURL, ChromaURL: cURL, Embedder: "m", DryRun: true,
	})
	if err != nil {
		t.Fatalf("Index dry-run: %v", err)
	}
	if stats.ChunksRead != 3 || stats.ChunksUpserted != 0 {
		t.Errorf("dry-run stats = %+v, want read=3 upserted=0", stats)
	}
}

func TestIndex_OnlyScopesFilter(t *testing.T) {
	corpusDir := t.TempDir()
	writeCorpusNDJSON(t, corpusDir, "backend", 2)
	writeCorpusNDJSON(t, corpusDir, "webapp", 2)
	upserts := 0
	oURL, cURL := corpusIndexServers(t, &upserts)
	stats, err := Index(context.Background(), IndexConfig{
		CorpusDir: corpusDir, OllamaURL: oURL, ChromaURL: cURL, Embedder: "m",
		OnlyScopes: []string{"backend"},
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.ChunksUpserted != 2 {
		t.Errorf("only-scope upserted = %d, want 2 (backend only)", stats.ChunksUpserted)
	}
}

func TestIndex_OllamaUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	_, err := Index(context.Background(), IndexConfig{
		CorpusDir: t.TempDir(), OllamaURL: dead.URL, ChromaURL: dead.URL, Embedder: "m",
	})
	if err == nil || !strings.Contains(err.Error(), "ollama unreachable") {
		t.Errorf("want ollama-unreachable error, got %v", err)
	}
}
