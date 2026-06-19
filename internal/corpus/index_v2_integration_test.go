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

func v2Servers(t *testing.T, upserts *int) (oURL, cURL string) {
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
			_, _ = w.Write([]byte(`{"id":"v2","name":"olifant-v2-curriculum"}`))
		case strings.HasSuffix(r.URL.Path, "/upsert"):
			if upserts != nil {
				var req struct {
					IDs []string `json:"ids"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				*upserts += len(req.IDs)
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc text"]],"metadatas":[[{"repo":"core-api","item_kind":"symbol","source":"x.go"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

func writeV2Curriculum(t *testing.T, kb string) {
	t.Helper()
	root := filepath.Join(kb, "corpus", "v2-curriculum")
	vocab := filepath.Join(root, "vocab", "core-api")
	prose := filepath.Join(root, "prose", "core-api")
	for _, d := range []string{vocab, prose} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(vocab, "symbols.yaml"),
		[]byte("- id: sym1\n  text: TenantScoped\n  source: Foo.java\n  line: 4\n- id: sym2\n  text: SQLDelete\n  source: Foo.java\n  line: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prose, "sentences.yaml"),
		[]byte("- id: sen1\n  text: tenant id is stamped automatically\n  source: doc.md\n  line: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexV2_FullWithSmoke(t *testing.T) {
	kb := t.TempDir()
	writeV2Curriculum(t, kb)
	smokeOut := filepath.Join(t.TempDir(), "smoke.md")

	upserts := 0
	oURL, cURL := v2Servers(t, &upserts)
	stats, err := IndexV2(context.Background(), IndexV2Config{
		KBRoot:    kb,
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "bge-m3",
		Smoke:     true,
		SmokeOut:  smokeOut,
		Verbose:   true,
	})
	if err != nil {
		t.Fatalf("IndexV2: %v", err)
	}
	if stats.SymbolsRead != 2 || stats.SentencesRead != 1 {
		t.Errorf("read counts = symbols:%d sentences:%d, want 2/1", stats.SymbolsRead, stats.SentencesRead)
	}
	if stats.ChunksUpserted != 3 {
		t.Errorf("ChunksUpserted = %d, want 3", stats.ChunksUpserted)
	}
	if upserts != 3 {
		t.Errorf("chroma saw %d ids, want 3", upserts)
	}
	if len(stats.Smoke) == 0 {
		t.Error("expected smoke results")
	}
	if _, err := os.Stat(smokeOut); err != nil {
		t.Errorf("smoke report not written: %v", err)
	}
}

func TestIndexV2_DryRun(t *testing.T) {
	kb := t.TempDir()
	writeV2Curriculum(t, kb)
	stats, err := IndexV2(context.Background(), IndexV2Config{KBRoot: kb, DryRun: true})
	if err != nil {
		t.Fatalf("IndexV2 dry-run: %v", err)
	}
	if stats.SymbolsRead != 2 || stats.ChunksUpserted != 0 {
		t.Errorf("dry-run stats = %+v", stats)
	}
}

func TestIndexV2_OnlyVocab(t *testing.T) {
	kb := t.TempDir()
	writeV2Curriculum(t, kb)
	upserts := 0
	oURL, cURL := v2Servers(t, &upserts)
	stats, err := IndexV2(context.Background(), IndexV2Config{
		KBRoot: kb, OllamaURL: oURL, ChromaURL: cURL, Embedder: "m",
		OnlyKinds: []string{"vocab"},
	})
	if err != nil {
		t.Fatalf("IndexV2: %v", err)
	}
	if stats.SentencesRead != 0 || stats.SymbolsRead != 2 {
		t.Errorf("only-vocab stats = symbols:%d sentences:%d", stats.SymbolsRead, stats.SentencesRead)
	}
}

func TestIndexV2_MissingRootErrors(t *testing.T) {
	_, err := IndexV2(context.Background(), IndexV2Config{KBRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "v2-curriculum root") {
		t.Errorf("missing curriculum root should error, got %v", err)
	}
}
