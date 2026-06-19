package challenge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/synth"
)

type fakeSynth struct{ out string }

func (f fakeSynth) Generate(ctx context.Context, req synth.Request) (*synth.Response, error) {
	return &synth.Response{Text: f.out, EvalCount: 7, EvalDuration: 1e9}, nil
}

// challengeServers stands up fake Ollama (embed) + Chroma (collections+query).
func challengeServers(t *testing.T) (ollamaURL, chromaURL string) {
	t.Helper()
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
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
			_, _ = w.Write([]byte(`{"id":"c1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc body"]],"metadatas":[[{"source":"patterns/backend.md","scope":"backend","item_kind":"rule"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

const cleanChallengeJSON = `{"challenge":{
	"request":"add a tenant scoped invoice entity",
	"verdict":"VALID",
	"proceed":"proceed_directly",
	"confirms":[{"claim":"ok","cites":["SB-04"]}],
	"applicable_rules":{"standards":["D154"],"patterns":[],"anti_patterns_to_avoid":[],"decisions_to_honor":[]}
}}`

func TestRun_V1_HappyPath(t *testing.T) {
	oURL, cURL := challengeServers(t)
	v := buildKBValidator(t)

	res, err := Run(context.Background(), Config{
		Request:     "add a tenant scoped invoice entity",
		OllamaURL:   oURL,
		ChromaURL:   cURL,
		Embedder:    "bge-m3",
		Synthesizer: "m",
		Scopes:      []string{"backend"},
		Validator:   v,
		Synth:       fakeSynth{out: cleanChallengeJSON},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.JSONValid {
		t.Error("expected valid JSON output")
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieval hits")
	}
	if v, p := res.ExtractVerdict(); v != "VALID" || p != "proceed_directly" {
		t.Errorf("verdict = (%q,%q)", v, p)
	}
}

func TestRun_V2_RetrievalPath(t *testing.T) {
	oURL, cURL := challengeServers(t)

	res, err := Run(context.Background(), Config{
		Request:      "add a tenant scoped invoice entity",
		OllamaURL:    oURL,
		ChromaURL:    cURL,
		Embedder:     "bge-m3",
		Synthesizer:  "m",
		Scopes:       []string{"backend"},
		V2Collection: "kb_v2",
		Synth:        fakeSynth{out: cleanChallengeJSON},
	})
	if err != nil {
		t.Fatalf("Run v2: %v", err)
	}
	if res.RetrievedCount == 0 {
		t.Error("expected v2 retrieval hits")
	}
}
