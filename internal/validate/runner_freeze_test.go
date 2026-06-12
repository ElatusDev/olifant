package validate

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
// this body (workflow D-F4-2, prompt Hard Rule 2). Without a Validator the
// pipeline is a single synth call with no retrieval.
func TestRunGenerateRequestFrozen(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		got, _ = io.ReadAll(r.Body)
		_, _ = fmt.Fprint(w, `{"response":"{}","done":true,"eval_count":3,"eval_duration":1000}`)
	}))
	defer srv.Close()

	cfg := Config{
		Claim:       "the fix resolves the tenant leak",
		Diff:        "diff --git a/x b/x",
		OllamaURL:   srv.URL,
		Synthesizer: "synth-m",
		Temperature: 0.1,
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
		Prompt: buildPrompt(cfg.Claim, cfg.Diff, nil),
		System: systemPrompt,
		Stream: false,
		Options: map[string]interface{}{
			"temperature": 0.1,
			"num_predict": 700,
		},
		Format: BuildValidateSchema(nil, nil),
	})
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generate request body changed:\n got: %s\nwant: %s", got, want)
	}
}
