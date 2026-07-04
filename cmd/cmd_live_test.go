//go:build integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ElatusDev/olifant/internal/livetest"
)

// TestLive_CmdCorpusIndex drives the `corpus index` command end-to-end against
// the live stack: it indexes a tiny NDJSON corpus into an isolated corpus_itest
// collection (real embed + upsert via config.Resolve's live endpoints).
func TestLive_CmdCorpusIndex(t *testing.T) {
	livetest.RequireStack(t)

	corpusDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(corpusDir, "itest.ndjson"),
		[]byte(`{"chunk_id":"cmd-itest-1","source":"itest/x.md","scope":"itest","doc_type":"doc","body":"Tenant scoping uses a PRE_INSERT listener."}`+"\n"), 0o644)

	code := corpusIndex([]string{"-kb-root", t.TempDir(), "-corpus-dir", corpusDir})
	if code != 0 {
		t.Errorf("corpus index = %d, want 0", code)
	}
}

// TestLive_CmdChallenge drives the `challenge` command end-to-end against the
// live stack with the Ollama synth backend (no claude, no ledger write).
func TestLive_CmdChallenge(t *testing.T) {
	livetest.RequireStack(t)
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")

	code := Challenge([]string{
		"-no-record",
		"-scopes", "backend",
		// 480s: at ~10 tok/s on the mini a verdict + one blocker retry can
		// consume a 240s budget entirely (observed 240.02s pass — no headroom).
		"-timeout", "480",
		"Add a TenantScoped invoice entity to core-api with a composite key",
	})
	if code != 0 {
		t.Errorf("challenge = %d, want 0", code)
	}
}

// TestLive_CmdPromptContext drives `prompt context` against the live stack —
// the retrieval-only grounding path (charter R2, D-OP1: embed+retrieve, no
// synthesis, so this is seconds-cheap even on the mini).
func TestLive_CmdPromptContext(t *testing.T) {
	livetest.RequireStack(t)

	if code := promptContext([]string{
		"-no-record",
		"-scope", "backend",
		"-top", "5",
		"how is tenant scoping enforced on entities",
	}); code != 0 {
		t.Errorf("prompt context = %d, want 0", code)
	}
}

// TestLive_CmdPromptCheck runs the cite gate against the real knowledge base
// (offline path — needs the KB checkout, not the stack) on a real charter doc.
func TestLive_CmdPromptCheck(t *testing.T) {
	_, kbRoot := livetest.RequireKB(t)

	doc := filepath.Join(kbRoot, "olifant", "CHARTER.md")
	if _, err := os.Stat(doc); err != nil {
		t.Skipf("charter doc not present in this KB checkout: %v", err)
	}
	code := promptCheck([]string{"-no-record", "-v", doc})
	if code == 2 {
		t.Fatalf("prompt check setup error (exit 2)")
	}
	t.Logf("prompt check on %s exit=%d (1 = unresolved cites found — informative, gate is advisory)", doc, code)
}

// TestLive_CmdEvalRun drives `eval run` end-to-end (the gate's pipeline) on a
// one-case suite against the live stack with the Ollama synth backend.
func TestLive_CmdEvalRun(t *testing.T) {
	livetest.RequireStack(t)
	livetest.RequireKB(t)
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")

	suite := filepath.Join(t.TempDir(), "suite.yaml")
	_ = os.WriteFile(suite, []byte("suite_id: cmd-itest\ncases:\n  - id: c1\n    scope: [backend]\n    request: Add a TenantScoped invoice entity with a composite key\n"), 0o644)

	code := evalRun([]string{"-suite", suite, "-out", t.TempDir()})
	if code != 0 {
		t.Errorf("eval run = %d, want 0", code)
	}
}
