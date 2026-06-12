package config

import (
	"strings"
	"testing"
)

func TestResolve_SynthBackendDefault(t *testing.T) {
	t.Setenv("OLIFANT_SYNTH_BACKEND", "")
	rt := Resolve()
	if rt.SynthBackend != "ollama" {
		t.Errorf("SynthBackend default = %q, want ollama", rt.SynthBackend)
	}
}

func TestResolve_SynthBackendOverride(t *testing.T) {
	t.Setenv("OLIFANT_SYNTH_BACKEND", "claude")
	rt := Resolve()
	if rt.SynthBackend != "claude" {
		t.Errorf("SynthBackend = %q, want claude", rt.SynthBackend)
	}
	if !strings.Contains(rt.String(), "backend=claude") {
		t.Errorf("String() missing backend: %s", rt.String())
	}
}
