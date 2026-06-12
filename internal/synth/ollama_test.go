package synth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestOllamaGenerateWireShape(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		got, _ = io.ReadAll(r.Body)
		_, _ = fmt.Fprint(w, `{"response":"out","done":true,"eval_count":7,"eval_duration":42}`)
	}))
	defer srv.Close()

	req := Request{
		Model:       "m",
		System:      "sys",
		Prompt:      "p",
		Schema:      map[string]interface{}{"type": "object"},
		Temperature: 0.1,
		MaxTokens:   700,
	}
	resp, err := NewOllama(srv.URL).Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "out" || resp.EvalCount != 7 || resp.EvalDuration != 42 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}

	want, err := json.Marshal(ollama.GenerateRequest{
		Model:  "m",
		Prompt: "p",
		System: "sys",
		Stream: false,
		Options: map[string]interface{}{
			"temperature": 0.1,
			"num_predict": 700,
		},
		Format: map[string]interface{}{"type": "object"},
	})
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestOllamaGenerateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := NewOllama(srv.URL).Generate(context.Background(), Request{Model: "m", Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}
