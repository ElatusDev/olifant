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
//   OLIFANT_OLLAMA_URL        e.g., http://100.94.233.106:11434 (default)
//   OLIFANT_CHROMA_URL        e.g., http://localhost:8000 (default — assumes port-forward)
//   OLIFANT_EMBEDDER          default: nomic-embed-text
//   OLIFANT_SYNTHESIZER       default: qwen2.5:14b-instruct-q6_K
//   OLIFANT_CHROMA_TENANT     default: default_tenant
//   OLIFANT_CHROMA_DATABASE   default: default_database
type Runtime struct {
	OllamaURL       string
	ChromaURL       string
	Embedder        string
	Synthesizer     string
	ChromaTenant    string
	ChromaDatabase  string
}

// Resolve returns runtime config with env-var overrides applied.
func Resolve() Runtime {
	r := Runtime{
		OllamaURL:      env("OLIFANT_OLLAMA_URL", "http://100.94.233.106:11434"),
		ChromaURL:      env("OLIFANT_CHROMA_URL", "http://localhost:8000"),
		Embedder:       env("OLIFANT_EMBEDDER", "nomic-embed-text"),
		Synthesizer:    env("OLIFANT_SYNTHESIZER", "qwen2.5:14b-instruct-q6_K"),
		ChromaTenant:   env("OLIFANT_CHROMA_TENANT", "default_tenant"),
		ChromaDatabase: env("OLIFANT_CHROMA_DATABASE", "default_database"),
	}
	r.OllamaURL = strings.TrimRight(r.OllamaURL, "/")
	r.ChromaURL = strings.TrimRight(r.ChromaURL, "/")
	return r
}

// String dumps non-secret config for logging.
func (r Runtime) String() string {
	return fmt.Sprintf("ollama=%s chroma=%s embedder=%s synth=%s tenant=%s/db=%s",
		r.OllamaURL, r.ChromaURL, r.Embedder, r.Synthesizer, r.ChromaTenant, r.ChromaDatabase)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
