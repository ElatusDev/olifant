//go:build integration

// Package livetest provides skip-guarded access to the live external seams
// (Ollama, ChromaDB, the claude CLI) for integration tests. It is compiled
// only under the `integration` build tag, so it never bloats normal builds or
// the default `go test ./...` run.
//
// Each Require* guard calls t.Skip (not t.Fatal) when its dependency is
// unreachable, so the integration suite degrades to "skipped" on a laptop
// without the stack up rather than failing.
package livetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// probeTimeout bounds each reachability probe so a down dependency skips fast.
const probeTimeout = 5 * time.Second

// Runtime returns the resolved endpoint/model configuration (config.Resolve),
// honoring the same OLIFANT_* env overrides the binary uses.
func Runtime() config.Runtime { return config.Resolve() }

// RequireOllama skips the test unless the configured Ollama is reachable.
func RequireOllama(t *testing.T) config.Runtime {
	t.Helper()
	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if _, err := ollama.New(rt.OllamaURL).Version(ctx); err != nil {
		t.Skipf("ollama unreachable at %s: %v", rt.OllamaURL, err)
	}
	return rt
}

// RequireChroma skips the test unless the configured ChromaDB is reachable.
func RequireChroma(t *testing.T) config.Runtime {
	t.Helper()
	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
	if _, err := cc.Heartbeat(ctx); err != nil {
		t.Skipf("chroma unreachable at %s: %v (kubectl -n platform port-forward deploy/chromadb 8000:8000)", rt.ChromaURL, err)
	}
	return rt
}

// RequireStack skips unless BOTH Ollama and Chroma are reachable.
func RequireStack(t *testing.T) config.Runtime {
	t.Helper()
	RequireOllama(t)
	return RequireChroma(t)
}

// RequireClaude skips unless the claude CLI is on PATH (the cloud synth seam).
func RequireClaude(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH (cloud synth seam unavailable)")
	}
}

// Chroma returns a client pointed at the live ChromaDB with tenant + database
// ensured. Caller has already passed RequireChroma.
func Chroma(t *testing.T, rt config.Runtime) *chroma.Client {
	t.Helper()
	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := cc.EnsureTenant(ctx); err != nil {
		t.Fatalf("ensure tenant: %v", err)
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		t.Fatalf("ensure database: %v", err)
	}
	return cc
}

// Ollama returns a client pointed at the live Ollama. Caller has already
// passed RequireOllama.
func Ollama(rt config.Runtime) *ollama.Client { return ollama.New(rt.OllamaURL) }

// RequireKB walks up from the working directory for knowledge-base/README.md
// and returns (platformRoot, kbRoot). It skips the test when the KB is not
// found (e.g. a standalone checkout), so KB-grounded pipeline tests degrade to
// "skipped" rather than failing.
func RequireKB(t *testing.T) (platformRoot, kbRoot string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Skipf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(cwd, "knowledge-base", "README.md")
		if _, err := os.Stat(candidate); err == nil {
			kbRoot = filepath.Dir(candidate)
			return filepath.Dir(kbRoot), kbRoot
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			t.Skip("knowledge-base not found in cwd ancestors (run from the platform tree)")
		}
		cwd = parent
	}
}
