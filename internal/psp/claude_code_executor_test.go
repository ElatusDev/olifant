package psp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaudeBinary writes a shell script to a temp dir that emits the
// given stdout and exits with the given code. Optionally captures the
// args it received into a side-channel file so tests can assert on them.
//
// The script writes its argv (one arg per line) to <dir>/argv when
// captureArgs is true. Newlines inside arg values get NUL-escaped to keep
// the line-per-arg structure intact.
func fakeClaudeBinary(t *testing.T, stdout string, exitCode int, captureArgs bool) (binPath string, argvPath string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-claude")
	argvPath = filepath.Join(dir, "argv")

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	if captureArgs {
		fmt.Fprintf(&script, "for arg in \"$@\"; do printf '%%s\\n' \"$arg\"; done > %s\n", shellEscape(argvPath))
	}
	// Write stdout via a heredoc so it can contain any chars.
	script.WriteString("cat <<'STUB_EOF'\n")
	script.WriteString(stdout)
	if !strings.HasSuffix(stdout, "\n") {
		script.WriteString("\n")
	}
	script.WriteString("STUB_EOF\n")
	fmt.Fprintf(&script, "exit %d\n", exitCode)

	if err := os.WriteFile(binPath, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("fakeClaudeBinary write: %v", err)
	}
	return binPath, argvPath
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// structuredClaudeOutput returns a JSON body shaped like the CLI emits when
// --json-schema was passed: empty `result`, populated `structured_output`.
func structuredClaudeOutput(structured map[string]any, usage map[string]int) string {
	u := map[string]int{
		"input_tokens":                12,
		"output_tokens":               8,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
	}
	for k, v := range usage {
		u[k] = v
	}
	body, _ := json.Marshal(map[string]any{
		"type":              "result",
		"subtype":           "success",
		"is_error":          false,
		"duration_ms":       1234,
		"num_turns":         2,
		"result":            "",
		"structured_output": structured,
		"stop_reason":       "end_turn",
		"session_id":        "sess-test",
		"usage":             u,
	})
	return string(body)
}

// canonicalClaudeOutput returns a well-formed `claude -p --output-format json`
// body. Uses json.Marshal so the result string is escaped correctly even when
// it contains newlines, fences, or quotes.
func canonicalClaudeOutput(result string, usage map[string]int) string {
	u := map[string]int{
		"input_tokens":                12,
		"output_tokens":               8,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
	}
	for k, v := range usage {
		u[k] = v
	}
	body, _ := json.Marshal(map[string]any{
		"type":        "result",
		"subtype":     "success",
		"is_error":    false,
		"duration_ms": 1234,
		"num_turns":   1,
		"result":      result,
		"stop_reason": "end_turn",
		"session_id":  "sess-test",
		"usage":       u,
	})
	return string(body)
}

func TestClaudeCodeExecutor_ID_ReturnsModel(t *testing.T) {
	e := NewClaudeCodeExecutor("/bin/echo", "claude-sonnet-4-6", "", 0, "")
	if e.ID() != "claude-sonnet-4-6" {
		t.Errorf("ID()=%q want claude-sonnet-4-6", e.ID())
	}
}

func TestClaudeCodeExecutor_Execute_HappyPath(t *testing.T) {
	bin, _ := fakeClaudeBinary(t, canonicalClaudeOutput(`{"verdict":"pass"}`, nil), 0, false)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "", 30*time.Second, "")

	schema := map[string]interface{}{
		"type":     "object",
		"required": []string{"verdict"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := e.Execute(ctx, "test prompt", schema)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.RawText != `{"verdict":"pass"}` {
		t.Errorf("RawText=%q", resp.RawText)
	}
	if resp.Output["verdict"] != "pass" {
		t.Errorf("Output[verdict]=%v want pass", resp.Output["verdict"])
	}
	if resp.OutputTokens != 8 {
		t.Errorf("OutputTokens=%d want 8", resp.OutputTokens)
	}
	if resp.TotalDurationNs <= 0 {
		t.Errorf("TotalDurationNs=%d should be measured", resp.TotalDurationNs)
	}
}

func TestClaudeCodeExecutor_Execute_CapturesCacheUsage(t *testing.T) {
	bin, _ := fakeClaudeBinary(t, canonicalClaudeOutput(`{}`, map[string]int{
		"input_tokens":                12,
		"output_tokens":               4,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     2048,
	}), 0, false)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "", 30*time.Second, "")

	resp, err := e.Execute(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.CacheReadTokens != 2048 {
		t.Errorf("CacheReadTokens=%d want 2048", resp.CacheReadTokens)
	}
	if resp.PromptTokens != 12+0+2048 {
		t.Errorf("PromptTokens=%d want %d (sum of input + cache create + cache read)",
			resp.PromptTokens, 12+0+2048)
	}
}

func TestClaudeCodeExecutor_Execute_PassesCoreFlags(t *testing.T) {
	bin, argvPath := fakeClaudeBinary(t, canonicalClaudeOutput(`{}`, nil), 0, true)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "high", 30*time.Second, "")

	schema := map[string]interface{}{"type": "object", "required": []string{"x"}}
	if _, err := e.Execute(context.Background(), "hello", schema); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	argSet := make(map[string]string, len(args))
	for i := 0; i < len(args); i++ {
		argSet[args[i]] = ""
		if i+1 < len(args) {
			argSet[args[i]] = args[i+1]
		}
	}

	for _, want := range []string{"-p", "--output-format", "--tools", "--no-session-persistence",
		"--permission-mode", "--model", "--effort", "--system-prompt", "--json-schema"} {
		if _, ok := argSet[want]; !ok {
			t.Errorf("missing flag %q in argv: %v", want, args)
		}
	}
	if argSet["--output-format"] != "json" {
		t.Errorf("--output-format=%q want json", argSet["--output-format"])
	}
	if argSet["--model"] != "claude-sonnet-4-6" {
		t.Errorf("--model=%q want claude-sonnet-4-6", argSet["--model"])
	}
	if argSet["--effort"] != "high" {
		t.Errorf("--effort=%q want high", argSet["--effort"])
	}
	if argSet["--permission-mode"] != "bypassPermissions" {
		t.Errorf("--permission-mode=%q want bypassPermissions", argSet["--permission-mode"])
	}
	if !strings.Contains(argSet["--json-schema"], `"required"`) {
		t.Errorf("--json-schema should carry the schema, got %q", argSet["--json-schema"])
	}
}

func TestClaudeCodeExecutor_Execute_OmitsEffortAndSchemaWhenUnset(t *testing.T) {
	bin, argvPath := fakeClaudeBinary(t, canonicalClaudeOutput(`hello`, nil), 0, true)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "", 30*time.Second, "")

	if _, err := e.Execute(context.Background(), "p", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	argv, _ := os.ReadFile(argvPath)
	body := string(argv)
	if strings.Contains(body, "--effort") {
		t.Errorf("--effort should be absent when not configured: %s", body)
	}
	if strings.Contains(body, "--json-schema") {
		t.Errorf("--json-schema should be absent when schema is empty: %s", body)
	}
}

func TestClaudeCodeExecutor_Execute_StructuredOutputPath(t *testing.T) {
	// When --json-schema is set, the CLI returns structured_output (object)
	// with an empty result string. Executor must read structured_output.
	body := structuredClaudeOutput(map[string]any{
		"verdict": "pass",
		"reasons": []string{"a", "b", "c"},
	}, nil)
	bin, _ := fakeClaudeBinary(t, body, 0, false)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "", 30*time.Second, "")

	schema := map[string]interface{}{"type": "object", "required": []string{"verdict"}}
	resp, err := e.Execute(context.Background(), "p", schema)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Output["verdict"] != "pass" {
		t.Errorf("Output[verdict]=%v want pass", resp.Output["verdict"])
	}
	reasons, ok := resp.Output["reasons"].([]any)
	if !ok || len(reasons) != 3 {
		t.Errorf("Output[reasons]=%v want 3-element array", resp.Output["reasons"])
	}
}

func TestClaudeCodeExecutor_Execute_EmptyResponseWithSchemaErrors(t *testing.T) {
	// Pathological case: schema requested but CLI returns neither result
	// nor structured_output. Must surface a clear error, not silently
	// produce an empty Output map.
	body := canonicalClaudeOutput("", nil) // empty result, no structured_output
	bin, _ := fakeClaudeBinary(t, body, 0, false)
	e := NewClaudeCodeExecutor(bin, "m", "", 30*time.Second, "")
	_, err := e.Execute(context.Background(), "p", map[string]interface{}{"type": "object"})
	if err == nil {
		t.Fatal("expected error when schema given but CLI returned no body")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error should describe the empty-response case: %v", err)
	}
}

func TestClaudeCodeExecutor_Execute_IsErrorReturnsError(t *testing.T) {
	body := `{"type":"result","subtype":"refusal","is_error":true,"result":"I cannot help with that","usage":{}}`
	bin, _ := fakeClaudeBinary(t, body, 0, false)
	e := NewClaudeCodeExecutor(bin, "claude-sonnet-4-6", "", 30*time.Second, "")
	_, err := e.Execute(context.Background(), "p", nil)
	if err == nil {
		t.Fatal("expected error when is_error=true")
	}
	if !strings.Contains(err.Error(), "refusal") {
		t.Errorf("error should mention subtype: %v", err)
	}
}

func TestClaudeCodeExecutor_Execute_NonZeroExit(t *testing.T) {
	bin, _ := fakeClaudeBinary(t, "boom", 7, false)
	e := NewClaudeCodeExecutor(bin, "m", "", 30*time.Second, "")
	_, err := e.Execute(context.Background(), "p", nil)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "claude code subprocess") {
		t.Errorf("error should identify the subprocess: %v", err)
	}
}

func TestClaudeCodeExecutor_Execute_GarbageStdout(t *testing.T) {
	bin, _ := fakeClaudeBinary(t, "not valid json at all", 0, false)
	e := NewClaudeCodeExecutor(bin, "m", "", 30*time.Second, "")
	_, err := e.Execute(context.Background(), "p", nil)
	if err == nil {
		t.Fatal("expected error on unparseable output")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error should mention unparseable output: %v", err)
	}
}

func TestClaudeCodeExecutor_Execute_TimeoutKillsSubprocess(t *testing.T) {
	// Sleep-forever script. `exec sleep` replaces the shell process so
	// SIGKILL hits sleep directly — without exec, the shell's child sleep
	// would orphan and we'd lose visibility into whether the timeout fired.
	dir := t.TempDir()
	bin := filepath.Join(dir, "sleepy-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write sleepy-claude: %v", err)
	}
	e := NewClaudeCodeExecutor(bin, "m", "", 200*time.Millisecond, "")
	start := time.Now()
	_, err := e.Execute(context.Background(), "p", nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout did not fire promptly: elapsed=%v", elapsed)
	}
}

func TestClaudeCodeExecutor_Execute_StripsCodeFence(t *testing.T) {
	// Result wrapped in a code fence (model misbehaves despite schema).
	body := canonicalClaudeOutput("```json\n{\"x\":1}\n```", nil)
	bin, _ := fakeClaudeBinary(t, body, 0, false)
	e := NewClaudeCodeExecutor(bin, "m", "", 30*time.Second, "")
	resp, err := e.Execute(context.Background(), "p", map[string]interface{}{"type": "object"})
	if err != nil {
		t.Fatalf("Execute: %v (raw=%q)", err, resp.RawText)
	}
	if resp.Output["x"] != float64(1) {
		t.Errorf("Output[x]=%v want 1 (post-fence-strip)", resp.Output["x"])
	}
}

func TestStripJSONFence(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"{}", "{}"},
		{"```json\n{}\n```", "{}"},
		{"```\n{}\n```", "{}"},
		{"  ```json\n{\"x\":1}\n```  ", `{"x":1}`},
		{`{"a":1}`, `{"a":1}`},
	} {
		got := stripJSONFence(tc.in)
		if got != tc.want {
			t.Errorf("stripJSONFence(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
