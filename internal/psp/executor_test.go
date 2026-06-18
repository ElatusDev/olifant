package psp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalExecutor_ID(t *testing.T) {
	e := NewLocalExecutor("http://localhost:11434", "qwen2.5:14b")
	if e.ID() != "qwen2.5:14b" {
		t.Errorf("ID()=%q want qwen2.5:14b", e.ID())
	}
}

func TestLocalExecutor_Execute_WithSchema(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		_, _ = w.Write([]byte(`{"response":"{\"verdict\":\"pass\"}","done":true,"eval_count":5,"prompt_eval_count":11,"total_duration":99}`))
	}))
	defer srv.Close()

	e := NewLocalExecutor(srv.URL, "m")
	resp, err := e.Execute(context.Background(), "p", map[string]interface{}{"type": "object"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Output["verdict"] != "pass" {
		t.Errorf("Output[verdict]=%v want pass", resp.Output["verdict"])
	}
	if resp.EvalTokens != 5 || resp.PromptTokens != 11 || resp.TotalDurationNs != 99 {
		t.Errorf("metric mapping wrong: %+v", resp)
	}
	// Schema present → grammar-constrained decoding requested via format.
	if !strings.Contains(gotBody, `"format"`) {
		t.Errorf("schema should be sent as format: %s", gotBody)
	}
}

func TestLocalExecutor_Execute_NoSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":"  free text  ","done":true,"eval_count":2}`))
	}))
	defer srv.Close()

	e := NewLocalExecutor(srv.URL, "m")
	resp, err := e.Execute(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.RawText != "free text" {
		t.Errorf("RawText=%q want trimmed 'free text'", resp.RawText)
	}
	if resp.Output != nil {
		t.Errorf("Output should be nil without a schema, got %v", resp.Output)
	}
}

func TestLocalExecutor_Execute_GenerateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := NewLocalExecutor(srv.URL, "m")
	_, err := e.Execute(context.Background(), "p", nil)
	if err == nil || !strings.Contains(err.Error(), "executor.Execute") {
		t.Fatalf("expected generate error, got %v", err)
	}
}

func TestLocalExecutor_Execute_InvalidJSONWithSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":"not json","done":true}`))
	}))
	defer srv.Close()

	e := NewLocalExecutor(srv.URL, "m")
	_, err := e.Execute(context.Background(), "p", map[string]interface{}{"type": "object"})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected JSON parse error, got %v", err)
	}
}

func TestClaudeCodeExecutor_WithSystemPrompt(t *testing.T) {
	bin, argvPath := fakeClaudeBinary(t, canonicalClaudeOutput(`{}`, nil), 0, true)
	e := NewClaudeCodeExecutor(bin, "m", "", 30*time.Second, "").WithSystemPrompt("custom-sys")
	if _, err := e.Execute(context.Background(), "p", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	argv := readArgv(t, argvPath)
	if got := argv["--system-prompt"]; got != "custom-sys" {
		t.Errorf("--system-prompt=%q want custom-sys", got)
	}
}

// readArgv parses a one-arg-per-line argv dump into a flag→value map.
func readArgv(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := make(map[string]string, len(args))
	for i := 0; i < len(args); i++ {
		out[args[i]] = ""
		if i+1 < len(args) {
			out[args[i]] = args[i+1]
		}
	}
	return out
}
