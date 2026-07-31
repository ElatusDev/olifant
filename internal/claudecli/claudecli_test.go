package claudecli

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
// stdout, optionally records its argv (one arg per line) to argvFile, and
// exits with code.
func fakeClaudeBinary(t *testing.T, output string, exitCode int, argvFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if argvFile != "" {
		fmt.Fprintf(&sb, "for arg in \"$@\"; do printf '%%s\\n' \"$arg\"; done > %q\n", argvFile)
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

func TestBuild_AllFlags(t *testing.T) {
	schema := map[string]interface{}{"type": "object"}
	args, err := Build(Args{
		Prompt: "the prompt",
		Model:  "claude-sonnet-4-6",
		Effort: "high",
		System: "sys",
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wantSchema, _ := json.Marshal(schema)
	want := []string{
		"-p", "the prompt",
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
		"--model", "claude-sonnet-4-6",
		"--effort", "high",
		"--system-prompt", "sys",
		"--json-schema", string(wantSchema),
	}
	if len(args) != len(want) {
		t.Fatalf("len(args)=%d want %d: %q", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d]=%q want %q", i, args[i], want[i])
		}
	}
}

func TestBuild_OmitsOptionalWhenUnset(t *testing.T) {
	args, err := Build(Args{Prompt: "p"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body := strings.Join(args, " ")
	for _, absent := range []string{"--model", "--effort", "--system-prompt", "--json-schema"} {
		if strings.Contains(body, absent) {
			t.Errorf("%q should be absent: %v", absent, args)
		}
	}
	// Core flags always present.
	for _, present := range []string{"-p", "--output-format", "--tools", "--no-session-persistence", "--permission-mode"} {
		if !strings.Contains(body, present) {
			t.Errorf("%q should be present: %v", present, args)
		}
	}
}

func TestRun_StructuredOutput(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv")
	bin := fakeClaudeBinary(t, `{"is_error":false,"structured_output":{"answer":"yes"},"usage":{"output_tokens":9,"input_tokens":12,"cache_read_input_tokens":2048}}`, 0, argvFile)

	res, err := Run(context.Background(), bin, Args{
		Prompt: "the prompt",
		Model:  "claude-sonnet-4-6",
		System: "sys",
		Schema: map[string]interface{}{"type": "object"},
	}, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Raw != `{"answer":"yes"}` {
		t.Errorf("Raw=%q", res.Raw)
	}
	if res.Usage.OutputTokens != 9 || res.Usage.InputTokens != 12 || res.Usage.CacheReadInputTokens != 2048 {
		t.Errorf("usage mapping wrong: %+v", res.Usage)
	}
	if res.ElapsedNs <= 0 {
		t.Errorf("ElapsedNs not measured: %d", res.ElapsedNs)
	}
}

func TestRun_FreeFormFenceStripped(t *testing.T) {
	out := `{"is_error":false,"result":"` + "```json\\n{\\\"a\\\":1}\\n```" + `","usage":{"output_tokens":2}}`
	bin := fakeClaudeBinary(t, out, 0, "")
	res, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Raw != `{"a":1}` {
		t.Fatalf("fence not stripped: %q", res.Raw)
	}
}

func TestRun_IsError(t *testing.T) {
	bin := fakeClaudeBinary(t, `{"is_error":true,"subtype":"error_during_execution","result":"boom"}`, 0, "")
	_, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "error_during_execution") {
		t.Fatalf("expected is_error failure, got %v", err)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	bin := fakeClaudeBinary(t, "rate limited", 2, "")
	_, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "subprocess") {
		t.Fatalf("expected subprocess failure, got %v", err)
	}
}

func TestRun_GarbageStdout(t *testing.T) {
	bin := fakeClaudeBinary(t, "not json at all", 0, "")
	_, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("expected parse failure, got %v", err)
	}
}

func TestRun_EmptyWithSchema(t *testing.T) {
	bin := fakeClaudeBinary(t, `{"is_error":false,"result":"","stop_reason":"end_turn"}`, 0, "")
	_, err := Run(context.Background(), bin, Args{
		Prompt: "p",
		Schema: map[string]interface{}{"type": "object"},
	}, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected empty-response failure, got %v", err)
	}
}

func TestRun_EmptyWithoutSchemaIsOK(t *testing.T) {
	// No schema → empty result is legal (free-form path), not an error.
	bin := fakeClaudeBinary(t, `{"is_error":false,"result":""}`, 0, "")
	res, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Raw != "" {
		t.Fatalf("Raw=%q want empty", res.Raw)
	}
}

func TestRun_WorkDirApplied(t *testing.T) {
	// A stub that prints its CWD as the result; assert Run ran it there.
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '{\"is_error\":false,\"result\":\"%s\"}\\n' \"$(pwd)\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{WorkDir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS /var → /private/var symlink: compare basename suffix.
	if !strings.HasSuffix(res.Raw, filepath.Base(dir)) {
		t.Fatalf("WorkDir not applied: pwd=%q want suffix %q", res.Raw, filepath.Base(dir))
	}
}

func TestRun_Timeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	start := time.Now()
	_, err := Run(context.Background(), path, Args{Prompt: "p"}, Options{Timeout: 150 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout did not kill subprocess promptly (%s)", time.Since(start))
	}
}

func TestStripFence(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"{}", "{}"},
		{"```json\n{}\n```", "{}"},
		{"```\n{}\n```", "{}"},
		{"  ```json\n{\"x\":1}\n```  ", `{"x":1}`},
		{`{"a":1}`, `{"a":1}`},
	} {
		if got := stripFence(tc.in); got != tc.want {
			t.Errorf("stripFence(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// fakeEnvBinary writes a stub that records the two env vars we care about
// (the cache flag + an inherited canary) then emits a minimal ok result.
func fakeEnvBinary(t *testing.T, envFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"printf '%s\\n%s\\n' \"$ENABLE_PROMPT_CACHING_1H\" \"$OLIFANT_TEST_CANARY\" > \"" + envFile + "\"\n" +
		"cat <<'OLIFANT_EOF'\n{\"is_error\":false,\"result\":\"ok\",\"usage\":{\"output_tokens\":1}}\nOLIFANT_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func TestRun_Cache1HSetsEnvAndPreservesInherited(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.txt")
	bin := fakeEnvBinary(t, envFile)
	t.Setenv("OLIFANT_TEST_CANARY", "inherited-ok")
	// Pin the parent to the CONFLICTING value: guaranteed-on relies on
	// os/exec's documented last-value-wins duplicate handling — the
	// appended "true" must beat an inherited "false".
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "false")

	if _, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{Cache1H: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env capture: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "true" {
		t.Errorf("ENABLE_PROMPT_CACHING_1H = %q, want \"true\" (Cache1H on)", lines[0])
	}
	if lines[1] != "inherited-ok" {
		t.Errorf("inherited env lost: canary = %q (D-4 append-not-replace violated)", lines[1])
	}
}

func TestRun_Cache1HOffDoesNotAddFlag(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.txt")
	bin := fakeEnvBinary(t, envFile)
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "")

	if _, err := Run(context.Background(), bin, Args{Prompt: "p"}, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env capture: %v", err)
	}
	if got := strings.Split(string(raw), "\n")[0]; got != "" {
		t.Errorf("ENABLE_PROMPT_CACHING_1H = %q with Cache1H off, want empty (inherit-only)", got)
	}
}
