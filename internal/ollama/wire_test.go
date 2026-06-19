package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c := New("http://host:11434/")
	if c.BaseURL != "http://host:11434" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.HTTP == nil || c.HTTP.Timeout == 0 {
		t.Error("HTTP client / timeout not initialised")
	}
}

func TestVersion(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"version":"0.5.1"}`)
	})
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "0.5.1" {
		t.Errorf("version = %q", v)
	}
}

func TestVersion_ServerError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("want error on 503")
	}
}

func TestEmbed_EmptyInputsShortCircuits(t *testing.T) {
	called := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	out, err := c.Embed(context.Background(), "bge-m3", nil)
	if err != nil || out != nil {
		t.Errorf("empty Embed = (%v,%v), want (nil,nil)", out, err)
	}
	if called {
		t.Error("empty Embed must not hit the server")
	}
}

func TestEmbed_SetsTruncateAndParses(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req EmbedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Truncate {
			t.Error("Truncate not set true")
		}
		if len(req.Input) != 2 {
			t.Errorf("inputs = %v", req.Input)
		}
		_ = json.NewEncoder(w).Encode(EmbedResponse{
			Model:      req.Model,
			Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		})
	})
	embs, err := c.Embed(context.Background(), "bge-m3", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embs) != 2 || embs[1][0] != 0.3 {
		t.Errorf("embeddings = %v", embs)
	}
}

func TestEmbed_CountMismatchErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Return only 1 vector for 2 inputs.
		_ = json.NewEncoder(w).Encode(EmbedResponse{Embeddings: [][]float32{{0.1}}})
	})
	_, err := c.Embed(context.Background(), "m", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "requested 2 embeddings, received 1") {
		t.Errorf("want count-mismatch error, got %v", err)
	}
}

func TestEmbed_ServerError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	})
	if _, err := c.Embed(context.Background(), "m", []string{"a"}); err == nil {
		t.Fatal("want error on 404")
	}
}

func TestGenerate_ForcesNonStreamAndParses(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req GenerateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			t.Error("Stream must be forced false")
		}
		_ = json.NewEncoder(w).Encode(GenerateResponse{
			Model: req.Model, Response: "hello", Done: true,
			EvalCount: 10, EvalDuration: 1e9,
		})
	})
	resp, err := c.Generate(context.Background(), GenerateRequest{Model: "qwen", Prompt: "hi", Stream: true})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Response != "hello" || !resp.Done {
		t.Errorf("resp = %+v", resp)
	}
}

func TestGenerate_ServerError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := c.Generate(context.Background(), GenerateRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("want error on 500")
	}
}

func TestTokensPerSec(t *testing.T) {
	if got := (&GenerateResponse{EvalCount: 100, EvalDuration: 2e9}).TokensPerSec(); got != 50 {
		t.Errorf("TokensPerSec = %v, want 50", got)
	}
	if got := (&GenerateResponse{EvalCount: 0, EvalDuration: 1e9}).TokensPerSec(); got != 0 {
		t.Errorf("zero EvalCount should give 0, got %v", got)
	}
	if got := (&GenerateResponse{EvalCount: 10, EvalDuration: 0}).TokensPerSec(); got != 0 {
		t.Errorf("zero EvalDuration should give 0, got %v", got)
	}
}

func TestDo_RequestBuildError(t *testing.T) {
	c := New("http://example.invalid")
	if err := c.do(context.Background(), "BAD\nMETHOD", "/x", nil, nil); err == nil {
		t.Error("want request-build error for invalid method")
	}
}

func TestDo_TransportError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	c := New(dead.URL)
	if _, err := c.Version(context.Background()); err == nil {
		t.Error("want transport error against closed server")
	}
}
