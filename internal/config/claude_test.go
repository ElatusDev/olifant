package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Env vars that ResolveClaude reads. Cleared at the start of each test so
// stray host env doesn't leak into the matrix.
var claudeEnv = []string{
	"OLIFANT_CLAUDE_BINARY",
	"OLIFANT_CLAUDE_MODEL",
	"OLIFANT_CLAUDE_EFFORT",
	"OLIFANT_CLAUDE_TIMEOUT",
	"OLIFANT_CLAUDE_WORKDIR",
}

func clearClaudeEnv(t *testing.T) {
	t.Helper()
	for _, k := range claudeEnv {
		t.Setenv(k, "")
	}
}

// makeFakeBinary creates a no-op executable for tests that need a real
// binary path. Returns the absolute path; cleanup is handled by t.TempDir.
func makeFakeBinary(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("makeFakeBinary: %v", err)
	}
	return path
}

func TestResolveClaude_BinaryMissing_ReturnsFalse(t *testing.T) {
	clearClaudeEnv(t)
	// Point at a path that definitely doesn't exist.
	t.Setenv("OLIFANT_CLAUDE_BINARY", "/nonexistent/path/to/claude-binary-xyz")
	cfg, ok := ResolveClaude()
	if ok {
		t.Fatalf("expected ok=false for missing binary, got cfg=%+v", cfg)
	}
}

func TestResolveClaude_ExplicitBinary_Resolves(t *testing.T) {
	clearClaudeEnv(t)
	fake := makeFakeBinary(t, "fake-claude")
	t.Setenv("OLIFANT_CLAUDE_BINARY", fake)
	cfg, ok := ResolveClaude()
	if !ok {
		t.Fatal("expected ok=true when explicit binary exists")
	}
	if cfg.Binary != fake {
		t.Errorf("Binary=%q want %q", cfg.Binary, fake)
	}
	if cfg.Model != DefaultClaudeModel {
		t.Errorf("Model=%q want default %q", cfg.Model, DefaultClaudeModel)
	}
	if cfg.Timeout != DefaultClaudeTimeout {
		t.Errorf("Timeout=%v want default %v", cfg.Timeout, DefaultClaudeTimeout)
	}
	if cfg.Effort != "" {
		t.Errorf("Effort=%q want empty (CLI default)", cfg.Effort)
	}
}

func TestResolveClaude_AllOverrides(t *testing.T) {
	clearClaudeEnv(t)
	fake := makeFakeBinary(t, "fake-claude")
	t.Setenv("OLIFANT_CLAUDE_BINARY", fake)
	t.Setenv("OLIFANT_CLAUDE_MODEL", "claude-opus-4-7")
	t.Setenv("OLIFANT_CLAUDE_EFFORT", "high")
	t.Setenv("OLIFANT_CLAUDE_TIMEOUT", "60")
	t.Setenv("OLIFANT_CLAUDE_WORKDIR", "/tmp/wd")

	cfg, ok := ResolveClaude()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.Model != "claude-opus-4-7" {
		t.Errorf("Model=%q want claude-opus-4-7", cfg.Model)
	}
	if cfg.Effort != "high" {
		t.Errorf("Effort=%q want high", cfg.Effort)
	}
	if cfg.Timeout != 60*time.Second {
		t.Errorf("Timeout=%v want 60s", cfg.Timeout)
	}
	if cfg.WorkDir != "/tmp/wd" {
		t.Errorf("WorkDir=%q want /tmp/wd", cfg.WorkDir)
	}
}

func TestResolveClaude_InvalidTimeout_FallsBackToDefault(t *testing.T) {
	clearClaudeEnv(t)
	fake := makeFakeBinary(t, "fake-claude")
	t.Setenv("OLIFANT_CLAUDE_BINARY", fake)
	t.Setenv("OLIFANT_CLAUDE_TIMEOUT", "garbage")
	cfg, _ := ResolveClaude()
	if cfg.Timeout != DefaultClaudeTimeout {
		t.Errorf("Timeout=%v want default on invalid input", cfg.Timeout)
	}
}

func TestClaudeConfig_String_Format(t *testing.T) {
	cfg := ClaudeConfig{
		Binary:  "/usr/local/bin/claude",
		Model:   "claude-sonnet-4-6",
		Effort:  "high",
		Timeout: 90 * time.Second,
		WorkDir: "/work",
	}
	s := cfg.String()
	for _, want := range []string{
		"/usr/local/bin/claude",
		"claude-sonnet-4-6",
		"high",
		"1m30s",
		"/work",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("String()=%q missing %q", s, want)
		}
	}

	// Empty effort/workdir render as placeholders, not blanks
	bare := ClaudeConfig{Binary: "/b", Model: "m", Timeout: time.Second}
	s = bare.String()
	if !strings.Contains(s, "(default)") {
		t.Errorf("String()=%q should show effort=(default) for empty effort", s)
	}
	if !strings.Contains(s, "(inherit)") {
		t.Errorf("String()=%q should show workdir=(inherit) for empty workdir", s)
	}
}

func TestEnvBool(t *testing.T) {
	for _, tc := range []struct {
		v    string
		def  bool
		want bool
	}{
		{"", true, true},
		{"", false, false},
		{"true", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		{"FALSE", true, false},
		{"  true  ", false, true},
		{"junk", true, true},
	} {
		t.Run(tc.v, func(t *testing.T) {
			t.Setenv("OLIFANT_TEST_BOOL", tc.v)
			got := envBool("OLIFANT_TEST_BOOL", tc.def)
			if got != tc.want {
				t.Errorf("envBool(%q, def=%v)=%v want %v", tc.v, tc.def, got, tc.want)
			}
		})
	}
}
