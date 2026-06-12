package challenge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// TestRunGenerateRequestFrozen freezes the exact wire bytes Run() sends to
// /api/generate. The F4.1 Synthesizer-interface refactor must not change
// this body (workflow D-F4-2, prompt Hard Rule 2). Chroma retrieval is
// pointed at the same fake server and 404s; the v1 retrieval path swallows
// that and proceeds with zero hits.
func TestRunGenerateRequestFrozen(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embed":
			_, _ = fmt.Fprint(w, `{"model":"e","embeddings":[[0.1,0.2]]}`)
		case "/api/generate":
			got, _ = io.ReadAll(r.Body)
			_, _ = fmt.Fprint(w, `{"response":"{}","done":true,"eval_count":3,"eval_duration":1000}`)
		default: // chroma queries — 404; retrieveV1 tolerates and yields no hits
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Request:     "does the newsfeed respect tenant scoping?",
		OllamaURL:   srv.URL,
		ChromaURL:   srv.URL,
		Embedder:    "e",
		Synthesizer: "synth-m",
		TopN:        6,
		Temperature: 0,
		MaxTokens:   700,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RawJSON != "{}" {
		t.Fatalf("response mapping changed: %+v", res)
	}

	want, err := json.Marshal(ollama.GenerateRequest{
		Model:  "synth-m",
		Prompt: buildChallengePrompt(cfg.Request, nil),
		System: systemPrompt,
		Stream: false,
		Options: map[string]interface{}{
			"temperature": 0.0,
			"num_predict": 700,
		},
		Format: BuildChallengeSchema(nil, unionWithUniversal(nil)),
	})
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generate request body changed:\n got: %s\nwant: %s", got, want)
	}
}
