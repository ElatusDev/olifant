package prompt

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

// TestSynthesizeRequestFrozen freezes the exact wire bytes synthesize()
// sends to /api/generate. The F4.1 Synthesizer-interface refactor must not
// change this body (workflow D-F4-2, prompt Hard Rule 2).
func TestSynthesizeRequestFrozen(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		got, _ = io.ReadAll(r.Body)
		_, _ = fmt.Fprint(w, `{"response":"{}","done":true,"eval_count":3,"eval_duration":1000}`)
	}))
	defer srv.Close()

	goal := "add a tenant-scoped newsfeed"
	hits := []Hit{}
	res, err := synthesize(context.Background(), synthConfig{
		OllamaURL:   srv.URL,
		Synthesizer: "synth-m",
		Temperature: 0.1,
		MaxTokens:   700,
	}, goal, hits)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.RawJSON != "{}" || res.EvalCount != 3 {
		t.Fatalf("response mapping changed: %+v", res)
	}

	want, err := json.Marshal(ollama.GenerateRequest{
		Model:  "synth-m",
		Prompt: buildPromptText(goal, hits),
		System: systemPrompt,
		Stream: false,
		Options: map[string]interface{}{
			"temperature": 0.1,
			"num_predict": 700,
		},
		Format: planSynthSchema(),
	})
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generate request body changed:\n got: %s\nwant: %s", got, want)
	}
}
