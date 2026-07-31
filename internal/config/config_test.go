package config

import (
	"strings"
	"testing"
)

func TestResolve_SynthBackendDefaultClaude(t *testing.T) {
	t.Setenv("OLIFANT_SYNTH_BACKEND", "")
	t.Setenv("OLIFANT_SYNTH_CLAUDE_MODEL", "")
	rt := Resolve()
	if rt.SynthBackend != "claude" {
		t.Errorf("SynthBackend default = %q, want claude (F4 Promote flip)", rt.SynthBackend)
	}
	if rt.SynthClaudeModel != "claude-sonnet-4-6" {
		t.Errorf("SynthClaudeModel default = %q, want claude-sonnet-4-6 (post-fable-5 retirement)", rt.SynthClaudeModel)
	}
}

func TestResolve_SynthBackendOllamaFallback(t *testing.T) {
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	rt := Resolve()
	if rt.SynthBackend != "ollama" {
		t.Errorf("SynthBackend = %q, want ollama", rt.SynthBackend)
	}
	if !strings.Contains(rt.String(), "backend=ollama") {
		t.Errorf("String() missing backend: %s", rt.String())
	}
}

func TestResolve_OllamaKeepAlive(t *testing.T) {
	t.Setenv("OLIFANT_OLLAMA_KEEP_ALIVE", "")
	if got := Resolve().OllamaKeepAlive; got != "30m" {
		t.Errorf("default OllamaKeepAlive = %q, want 30m", got)
	}
	t.Setenv("OLIFANT_OLLAMA_KEEP_ALIVE", "-1")
	if got := Resolve().OllamaKeepAlive; got != "-1" {
		t.Errorf("override OllamaKeepAlive = %q, want -1", got)
	}
}
