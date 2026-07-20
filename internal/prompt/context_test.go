package prompt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestBuildContext_HappyPath(t *testing.T) {
	oURL, cURL := retrievalServers(t)

	res, err := BuildContext(context.Background(), ContextConfig{
		Goal:      "tenant scoping",
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "m",
		Scopes:    []string{"backend"},
		TopN:      3,
	})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("no chunks returned")
	}
	c := res.Chunks[0]
	if c.Source != "patterns/backend.md" || c.Body != "chunk doc" {
		t.Errorf("chunk = %+v, want source patterns/backend.md body %q", c, "chunk doc")
	}
	if len(res.Sources) != 1 || res.Sources[0] != "patterns/backend.md" {
		t.Errorf("sources = %v", res.Sources)
	}
}

func TestBuildContext_ExtractsCitesAndCapsBody(t *testing.T) {
	oURL, _ := retrievalServers(t)
	longDoc := "Per D210 and AP164: " + strings.Repeat("x", 500)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"coll-1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["` + longDoc + `"]],"metadatas":[[{"source":"decisions/log.md","doc_type":"decision"}]],"distances":[[0.05]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)

	res, err := BuildContext(context.Background(), ContextConfig{
		Goal:      "g",
		OllamaURL: oURL,
		ChromaURL: chr.URL,
		Embedder:  "m",
		Scopes:    []string{"backend"},
		TopN:      1,
		MaxChars:  40,
	})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	c := res.Chunks[0]
	if len(c.Cites) != 2 || c.Cites[0] != "AP164" || c.Cites[1] != "D210" {
		t.Errorf("cites = %v, want [AP164 D210] (cites extracted from full doc)", c.Cites)
	}
	if len(c.Body) > 45 { // CapChars is rune-safe; allow slack for the ellipsis
		t.Errorf("body not capped: %d chars", len(c.Body))
	}
	if c.DocType != "decision" {
		t.Errorf("doc_type = %q, want decision", c.DocType)
	}
}

// recordingServers stands up a fake Ollama embedder + a Chroma that records
// every collection name ensured (queried). Returns the URLs and the recorder.
func recordingServers(t *testing.T) (ollamaURL, chromaURL string, ensured map[string]bool, mu *sync.Mutex) {
	t.Helper()
	ensured = map[string]bool{}
	mu = &sync.Mutex{}
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2, 0.3}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(oll.Close)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections"):
			var b struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			ensured[b.Name] = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"coll-1","name":"` + b.Name + `"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["d"]],"metadatas":[[{"source":"s"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL, ensured, mu
}

// D-PP3: ExtraFamilies opts the code-advice path into failure_modes, while the
// default retrieve path stays byte-for-byte unchanged (never queries it) and
// omits the code/history families the default set gates to code scopes.
func TestBuildContext_FamiliesOverride(t *testing.T) {
	oURL, cURL, ensured, mu := recordingServers(t)
	base := ContextConfig{
		Goal: "g", OllamaURL: oURL, ChromaURL: cURL, Embedder: "m",
		Scopes: []string{"backend"}, TopN: 3,
	}

	// Override with the rule-family set: queries corpus + failure_modes, and
	// NOT the code family (the whole point of the P3 refinement).
	rules := base
	rules.Families = []string{"corpus", "failure_modes"}
	if _, err := BuildContext(context.Background(), rules); err != nil {
		t.Fatalf("BuildContext(rule families): %v", err)
	}
	mu.Lock()
	gotFM, gotCorpus, leakedCode := ensured["failure_modes_backend"], ensured["corpus_backend"], ensured["code_backend"]
	mu.Unlock()
	if !gotFM || !gotCorpus {
		t.Errorf("rule-family override: corpus=%v failure_modes=%v, want both queried", gotCorpus, gotFM)
	}
	if leakedCode {
		t.Error("rule-family override still queried code_backend — must exclude source families")
	}

	// Default path: no failure_modes, but code_backend (a code scope) IS queried.
	// Clear in place — the server closure holds this map reference.
	mu.Lock()
	for k := range ensured {
		delete(ensured, k)
	}
	mu.Unlock()
	if _, err := BuildContext(context.Background(), base); err != nil {
		t.Fatalf("BuildContext(default): %v", err)
	}
	mu.Lock()
	defFM, defCode := ensured["failure_modes_backend"], ensured["code_backend"]
	mu.Unlock()
	if defFM {
		t.Error("default retrieve queried failure_modes — D-PP3 requires default behaviour unchanged")
	}
	if !defCode {
		t.Error("default retrieve did not query code_backend — default behaviour changed")
	}
}

func TestBuildContext_EmbedErrorPropagates(t *testing.T) {
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(oll.Close)

	_, err := BuildContext(context.Background(), ContextConfig{
		Goal: "x", OllamaURL: oll.URL, ChromaURL: oll.URL, Embedder: "m",
		Scopes: []string{"backend"},
	})
	if err == nil {
		t.Fatal("want embed error, got nil")
	}
}
