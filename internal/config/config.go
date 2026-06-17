// Package config centralizes runtime endpoints + model names so subcommands
// can be invoked from anywhere on the tailnet (laptop, mini, k8s pod) with
// the same code.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Runtime resolves connection endpoints + model selections.
// Override via env vars in this order:
//
//	OLIFANT_OLLAMA_URL        e.g., http://100.94.233.106:11434 (default)
//	OLIFANT_CHROMA_URL        e.g., http://localhost:8000 (default — assumes port-forward)
//	OLIFANT_EMBEDDER          default: bge-m3 (1024d; F2 #13 — +7.7% prose recall@5 over nomic-embed-text, held the eval gate 12/12/0B)
//	OLIFANT_SYNTHESIZER       default: qwen2.5:14b-instruct-q6_K
//	OLIFANT_SYNTH_BACKEND     default: claude (values: ollama | claude) — flipped at F4 Promote (gate GF4 PASS 2026-06-12); ollama remains the offline fallback
//	OLIFANT_SYNTH_CLAUDE_MODEL default: claude-sonnet-4-6 — production GA model; separate from OLIFANT_CLAUDE_MODEL (PSP executor). The original F4-promoted pin was claude-fable-5 (preview ID), retired by the CLI 2026-06-13 → 404 on every synth call; lesson logged as AP104 (avoid preview/codename pins in production defaults)
//	OLIFANT_CHROMA_TENANT     default: default_tenant
//	OLIFANT_CHROMA_DATABASE   default: default_database
type Runtime struct {
	OllamaURL        string
	ChromaURL        string
	Embedder         string
	Synthesizer      string
	SynthBackend     string
	SynthClaudeModel string
	ChromaTenant     string
	ChromaDatabase   string
}

// Resolve returns runtime config with env-var overrides applied.
func Resolve() Runtime {
	r := Runtime{
		OllamaURL:        env("OLIFANT_OLLAMA_URL", "http://100.94.233.106:11434"),
		ChromaURL:        env("OLIFANT_CHROMA_URL", "http://localhost:8000"),
		Embedder:         env("OLIFANT_EMBEDDER", "bge-m3"),
		Synthesizer:      env("OLIFANT_SYNTHESIZER", "qwen2.5:14b-instruct-q6_K"),
		SynthBackend:     env("OLIFANT_SYNTH_BACKEND", "claude"),
		SynthClaudeModel: env("OLIFANT_SYNTH_CLAUDE_MODEL", "claude-sonnet-4-6"),
		ChromaTenant:     env("OLIFANT_CHROMA_TENANT", "default_tenant"),
		ChromaDatabase:   env("OLIFANT_CHROMA_DATABASE", "default_database"),
	}
	r.OllamaURL = strings.TrimRight(r.OllamaURL, "/")
	r.ChromaURL = strings.TrimRight(r.ChromaURL, "/")
	return r
}

// String dumps non-secret config for logging.
func (r Runtime) String() string {
	return fmt.Sprintf("ollama=%s chroma=%s embedder=%s synth=%s backend=%s tenant=%s/db=%s",
		r.OllamaURL, r.ChromaURL, r.Embedder, r.Synthesizer, r.SynthBackend, r.ChromaTenant, r.ChromaDatabase)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
