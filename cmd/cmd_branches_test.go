package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_WithRetrieval(t *testing.T) {
	kb := kbTreeChdir(t)
	// A dictionary makes NewCiteValidator non-nil → the retrieval path runs.
	dict := filepath.Join(kb, "dictionary", "backend", "domain.yaml")
	if err := os.MkdirAll(filepath.Dir(dict), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dict, []byte("- term: SB-04\n- term: D154\n"), 0o644)

	fakeStack(t, validateJSON) // embed + query + generate
	patch := filepath.Join(t.TempDir(), "p.diff")
	_ = os.WriteFile(patch, []byte("diff --git a/x b/x\n+l\n"), 0o644)

	code := Validate([]string{"-v", "-claim-text", "added a test", "-diff", patch})
	if code != 0 {
		t.Errorf("Validate (with retrieval) = %d, want 0", code)
	}
}

func TestCorpusScan_ErrorBranches(t *testing.T) {
	t.Chdir(t.TempDir()) // outside platform tree → no autodetect
	if code := corpusScan(nil); code != 2 {
		t.Errorf("corpusScan(no repo) = %d, want 2", code)
	}
	// core-api requires --module.
	repo := t.TempDir()
	if code := corpusScan([]string{"-repo", "core-api", "-repo-root", repo, "-source-root", repo}); code != 2 {
		t.Errorf("corpusScan(core-api no module) = %d, want 2", code)
	}
	// Nonexistent repo-root.
	if code := corpusScan([]string{"-repo", "infra", "-repo-root", "/no/such/dir", "-module", "m"}); code != 1 {
		t.Errorf("corpusScan(bad repo-root) = %d, want 1", code)
	}
}

func TestCorpusProse_ErrorBranches(t *testing.T) {
	t.Chdir(t.TempDir())
	if code := corpusProse(nil); code != 2 {
		t.Errorf("corpusProse(no repo) = %d, want 2", code)
	}
	if code := corpusProse([]string{"-repo", "infra", "-repo-root", "/no/such/dir"}); code != 1 {
		t.Errorf("corpusProse(bad repo-root) = %d, want 1", code)
	}
}

func TestEval_Routing(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	suite := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	writeSuite(t, suite)

	// Eval → run
	if code := Eval([]string{"run", "-suite", suite, "-out", t.TempDir()}); code != 0 {
		t.Errorf("Eval(run) = %d, want 0", code)
	}
	// Eval → gate-check (no receipt → STALE exit 1)
	if code := Eval([]string{"gate-check", "-suite", suite}); code != gateExitFail {
		t.Errorf("Eval(gate-check) = %d, want %d", code, gateExitFail)
	}
}
