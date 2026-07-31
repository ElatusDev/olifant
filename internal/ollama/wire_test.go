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

// capturedBody returns a handler that records each request body and replies ok.
func capturedBody(t *testing.T, bodies *[]string, reply string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		*bodies = append(*bodies, string(raw))
		_, _ = io.WriteString(w, reply)
	}
}

func TestGenerate_KeepAliveInjection(t *testing.T) {
	var bodies []string
	c := newTestClient(t, capturedBody(t, &bodies, `{"response":"ok"}`))

	// Neither set → keep_alive absent entirely (byte-compat with today).
	if _, err := c.Generate(context.Background(), GenerateRequest{Model: "m", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodies[0], "keep_alive") {
		t.Errorf("keep_alive present with no config: %s", bodies[0])
	}

	// Client-level default fills.
	c.KeepAlive = "30m"
	if _, err := c.Generate(context.Background(), GenerateRequest{Model: "m", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[1], `"keep_alive":"30m"`) {
		t.Errorf("client default not injected: %s", bodies[1])
	}

	// Request-level wins over the client default.
	if _, err := c.Generate(context.Background(), GenerateRequest{Model: "m", Prompt: "p", KeepAlive: "-1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[2], `"keep_alive":"-1"`) {
		t.Errorf("request-level override lost: %s", bodies[2])
	}
}

func TestEmbed_KeepAliveInjection(t *testing.T) {
	var bodies []string
	c := newTestClient(t, capturedBody(t, &bodies, `{"embeddings":[[0.1]]}`))
	c.KeepAlive = "15m"
	if _, err := c.Embed(context.Background(), "bge-m3", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[0], `"keep_alive":"15m"`) {
		t.Errorf("embed keep_alive not injected: %s", bodies[0])
	}
}

func TestModelDigest(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"other:7b","model":"other:7b","digest":"sha-other"},
			{"name":"qwen3:8b","model":"qwen3:8b","digest":"sha-qwen"},
			{"name":"bare:latest","model":"bare:latest","digest":"sha-bare"}]}`)
	})
	ctx := context.Background()
	if d, err := c.ModelDigest(ctx, "qwen3:8b"); err != nil || d != "sha-qwen" {
		t.Errorf("ModelDigest(qwen3:8b) = %q, %v; want sha-qwen", d, err)
	}
	// A bare tag matches its `:latest` entry.
	if d, err := c.ModelDigest(ctx, "bare"); err != nil || d != "sha-bare" {
		t.Errorf("ModelDigest(bare) = %q, %v; want sha-bare", d, err)
	}
	// Unknown model: empty digest, no error — callers degrade to tag-only keying.
	if d, err := c.ModelDigest(ctx, "absent:1b"); err != nil || d != "" {
		t.Errorf("ModelDigest(absent) = %q, %v; want empty + nil", d, err)
	}
}

func TestModelDigest_EndpointDown(t *testing.T) {
	c := New("http://127.0.0.1:1")
	if _, err := c.ModelDigest(context.Background(), "qwen3:8b"); err == nil {
		t.Error("unreachable endpoint should return an error")
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
