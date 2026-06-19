package prompt

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
	"github.com/ElatusDev/olifant/internal/synth"
)

// fakeSynth is an injectable synth.Client returning canned JSON.
type fakeSynth struct {
	json string
	err  error
}

func (f fakeSynth) Generate(ctx context.Context, req synth.Request) (*synth.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &synth.Response{Text: f.json, EvalCount: 42, EvalDuration: 1e9}, nil
}

// retrievalServers stands up fake Ollama (embed) + Chroma (collections+query).
func retrievalServers(t *testing.T) (ollamaURL, chromaURL string) {
	t.Helper()
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
			_, _ = w.Write([]byte(`{"id":"coll-1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["chunk doc"]],"metadatas":[[{"source":"patterns/backend.md"}]],"distances":[[0.12]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

const validPlanJSON = `{"plan":{"goal":"Add invoices","scope":["backend"],"steps":[
  {"id":"step_01","name":"Survey","description":"read exemplars","signals":["patterns/backend.md"],"depends_on":[],"expected_output":{"type":"object","fields":["summary"]}},
  {"id":"step_02","name":"Design","description":"design key","depends_on":["step_01"],"expected_output":{"type":"object","fields":["id_class"]}}
]}}`

func TestBuild_HappyPath(t *testing.T) {
	oURL, cURL := retrievalServers(t)
	outDir := filepath.Join(t.TempDir(), "plans")

	res, err := Build(context.Background(), Config{
		Goal:      "Add invoices",
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "bge-m3",
		Scopes:    []string{"backend"},
		OutDir:    outDir,
		Synth:     fakeSynth{json: validPlanJSON},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.StepCount != 2 {
		t.Errorf("StepCount = %d, want 2", res.StepCount)
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieved hits")
	}
	if res.SynthEvalCount != 42 || res.SynthTokensSec == 0 {
		t.Errorf("synth telemetry not wired: eval=%d tps=%v", res.SynthEvalCount, res.SynthTokensSec)
	}
	if _, err := os.Stat(res.PlanPath); err != nil {
		t.Errorf("plan file not written: %v", err)
	}
}

func TestBuild_EmptyGoalErrors(t *testing.T) {
	_, err := Build(context.Background(), Config{Goal: "   "})
	if err == nil || !strings.Contains(err.Error(), "Goal is required") {
		t.Errorf("want goal-required error, got %v", err)
	}
}

func TestBuild_SynthErrorPropagates(t *testing.T) {
	oURL, cURL := retrievalServers(t)
	_, err := Build(context.Background(), Config{
		Goal: "x", OllamaURL: oURL, ChromaURL: cURL, Embedder: "m",
		Scopes: []string{"backend"},
		Synth:  fakeSynth{err: context.Canceled},
	})
	if err == nil {
		t.Error("synth error should propagate")
	}
}

func TestBuild_RetrieveEmbedErrorPropagates(t *testing.T) {
	// Ollama that 500s on embed → retrieve fails before synth.
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(oll.Close)
	_, err := Build(context.Background(), Config{
		Goal: "x", OllamaURL: oll.URL, ChromaURL: oll.URL, Embedder: "m",
		Scopes: []string{"backend"},
		Synth:  fakeSynth{json: validPlanJSON},
	})
	if err == nil {
		t.Error("embed failure should propagate")
	}
}

func TestDumpRequest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dbg.json")
	dumpRequest(p, ollama.GenerateRequest{Model: "m", Prompt: "hi"})
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("dump not written: %v", err)
	}
	if !strings.Contains(string(raw), `"model": "m"`) {
		t.Errorf("dump content unexpected:\n%s", raw)
	}
}

func TestSynthesize_UsesInjectedClient(t *testing.T) {
	sr, err := synthesize(context.Background(), synthConfig{Client: fakeSynth{json: validPlanJSON}},
		"goal", []Hit{{Doc: "d", Scope: "backend", Meta: map[string]interface{}{"source": "x"}}})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if !strings.Contains(sr.RawJSON, "step_01") {
		t.Errorf("raw json = %q", sr.RawJSON)
	}
	if sr.EvalCount != 42 {
		t.Errorf("eval count = %d", sr.EvalCount)
	}
}
