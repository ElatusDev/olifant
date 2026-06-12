package synth

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/config"
)

func TestFromRuntimeOllamaDefault(t *testing.T) {
	for _, backend := range []string{"", "ollama"} {
		rt := config.Runtime{SynthBackend: backend, OllamaURL: "http://x", Synthesizer: "qwen"}
		client, model, err := FromRuntime(rt)
		if err != nil {
			t.Fatalf("backend %q: %v", backend, err)
		}
		if _, ok := client.(*Ollama); !ok {
			t.Fatalf("backend %q: expected *Ollama, got %T", backend, client)
		}
		if model != "qwen" {
			t.Fatalf("backend %q: model = %q, want qwen", backend, model)
		}
	}
}

func TestFromRuntimeClaude(t *testing.T) {
	bin := fakeClaudeBinary(t, `{}`, 0, "")
	t.Setenv("OLIFANT_CLAUDE_BINARY", bin)
	t.Setenv("OLIFANT_CLAUDE_MODEL", "claude-fable-5")

	client, model, err := FromRuntime(config.Runtime{SynthBackend: "claude"})
	if err != nil {
		t.Fatalf("FromRuntime: %v", err)
	}
	if _, ok := client.(*Claude); !ok {
		t.Fatalf("expected *Claude, got %T", client)
	}
	if model != "claude-fable-5" {
		t.Fatalf("model = %q, want claude-fable-5", model)
	}
}

func TestFromRuntimeClaudeMissingBinary(t *testing.T) {
	t.Setenv("OLIFANT_CLAUDE_BINARY", filepath.Join(t.TempDir(), "nope"))
	_, _, err := FromRuntime(config.Runtime{SynthBackend: "claude"})
	if err == nil || !strings.Contains(err.Error(), "claude binary") {
		t.Fatalf("expected missing-binary error, got %v", err)
	}
}

func TestFromRuntimeUnknownBackend(t *testing.T) {
	_, _, err := FromRuntime(config.Runtime{SynthBackend: "gpt"})
	if err == nil || !strings.Contains(err.Error(), "unknown OLIFANT_SYNTH_BACKEND") {
		t.Fatalf("expected unknown-backend error, got %v", err)
	}
}
