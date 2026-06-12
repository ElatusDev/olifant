package synth

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

// fakeClaudeBinary writes an executable shell stub that emits `output` on
// stdout, optionally records its argv to argvFile, and exits with code.
func fakeClaudeBinary(t *testing.T, output string, exitCode int, argvFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if argvFile != "" {
		fmt.Fprintf(&sb, "printf '%%s\\n' \"$@\" > %q\n", argvFile)
	}
	sb.WriteString("cat <<'OLIFANT_EOF'\n")
	sb.WriteString(output)
	sb.WriteString("\nOLIFANT_EOF\n")
	fmt.Fprintf(&sb, "exit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func TestClaudeGenerateStructuredOutput(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv")
	bin := fakeClaudeBinary(t, `{"is_error":false,"structured_output":{"answer":"yes"},"usage":{"output_tokens":9}}`, 0, argvFile)

	schema := map[string]interface{}{"type": "object"}
	resp, err := NewClaude(bin, "high", time.Minute).Generate(context.Background(), Request{
		Model:  "claude-fable-5",
		System: "sys-prompt",
		Prompt: "the prompt",
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != `{"answer":"yes"}` || resp.EvalCount != 9 || resp.EvalDuration <= 0 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := strings.Split(strings.TrimRight(string(argvBytes), "\n"), "\n")
	wantSchema, _ := json.Marshal(schema)
	want := []string{
		"-p", "the prompt",
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
		"--model", "claude-fable-5",
		"--effort", "high",
		"--system-prompt", "sys-prompt",
		"--json-schema", string(wantSchema),
	}
	if len(argv) != len(want) {
		t.Fatalf("argv length %d != %d:\n%q", len(argv), len(want), argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestClaudeGenerateFreeFormFenceStripped(t *testing.T) {
	out := `{"is_error":false,"result":"` + "```json\\n{\\\"a\\\":1}\\n```" + `","usage":{"output_tokens":2}}`
	bin := fakeClaudeBinary(t, out, 0, "")
	resp, err := NewClaude(bin, "", time.Minute).Generate(context.Background(), Request{Prompt: "p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != `{"a":1}` {
		t.Fatalf("fence not stripped: %q", resp.Text)
	}
}

func TestClaudeGenerateIsError(t *testing.T) {
	bin := fakeClaudeBinary(t, `{"is_error":true,"subtype":"error_during_execution","result":"boom"}`, 0, "")
	_, err := NewClaude(bin, "", time.Minute).Generate(context.Background(), Request{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "error_during_execution") {
		t.Fatalf("expected is_error failure, got %v", err)
	}
}

func TestClaudeGenerateNonZeroExit(t *testing.T) {
	bin := fakeClaudeBinary(t, "rate limited", 2, "")
	_, err := NewClaude(bin, "", time.Minute).Generate(context.Background(), Request{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "subprocess") {
		t.Fatalf("expected subprocess failure, got %v", err)
	}
}

func TestClaudeGenerateGarbageStdout(t *testing.T) {
	bin := fakeClaudeBinary(t, "not json at all", 0, "")
	_, err := NewClaude(bin, "", time.Minute).Generate(context.Background(), Request{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("expected parse failure, got %v", err)
	}
}

func TestClaudeGenerateEmptyWithSchema(t *testing.T) {
	bin := fakeClaudeBinary(t, `{"is_error":false,"result":"","stop_reason":"end_turn"}`, 0, "")
	_, err := NewClaude(bin, "", time.Minute).Generate(context.Background(), Request{
		Prompt: "p",
		Schema: map[string]interface{}{"type": "object"},
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected empty-response failure, got %v", err)
	}
}

func TestClaudeGenerateTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	start := time.Now()
	_, err := NewClaude(path, "", 150*time.Millisecond).Generate(context.Background(), Request{Prompt: "p"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout did not kill subprocess promptly (%s)", time.Since(start))
	}
}
