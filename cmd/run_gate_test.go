package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ===== run (PSP executor) =====

func writeRunPlan(t *testing.T, dir string, stepCount int) string {
	t.Helper()
	body := "plan_id: 2026-06-19T08-05-03Z-run01\ngoal: do the thing\ncreated_by: test\nsteps:\n"
	for i := 1; i <= stepCount; i++ {
		body += fmt.Sprintf("  - id: step_%02d\n    description: do step\n    expected_output:\n      schema:\n        type: object\n", i)
	}
	p := filepath.Join(dir, "plan.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_Integration(t *testing.T) {
	// The local executor calls /api/generate per step; v0 validateStep only
	// requires parseable JSON, so any object output passes.
	fakeStack(t, `{"x":"done"}`)

	if code := Run(nil); code != 2 {
		t.Errorf("Run(nil) = %d, want 2", code)
	}
	if code := Run([]string{"-plan", filepath.Join(t.TempDir(), "missing.yaml")}); code != 1 {
		t.Errorf("Run(missing) = %d, want 1", code)
	}

	plan := writeRunPlan(t, t.TempDir(), 2)
	if code := Run([]string{"-plan", plan, "-v"}); code != 0 {
		t.Errorf("Run(valid) = %d, want 0", code)
	}
}

func TestRun_InvalidPlanOverCap(t *testing.T) {
	plan := writeRunPlan(t, t.TempDir(), 30) // > MaxStepsPerPlan (25)
	if code := Run([]string{"-plan", plan}); code != 1 {
		t.Errorf("Run(over-cap) = %d, want 1", code)
	}
}

// ===== eval gate-check =====

// kbTreeChdir creates a temp KB tree (knowledge-base/README.md), chdirs into a
// nested dir, and redirects HOME so receipt/drift writes stay hermetic.
// Returns the kb root path.
func kbTreeChdir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(filepath.Join(kb, "eval", "suites"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "README.md"), []byte("# KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// corpus manifest for the corpus fingerprint (best-effort in the command).
	if err := os.MkdirAll(filepath.Join(kb, "corpus", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(kb, "corpus", "v1", "manifest.yaml"), []byte("total_chunks: 0\n"), 0o644)
	nested := filepath.Join(kb, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	return kb
}

func writeSuite(t *testing.T, path string) {
	t.Helper()
	body := "suite_id: smoke\ncases:\n  - id: c1\n    scope: [backend]\n    request: add a tenant scoped invoice entity\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEvalGateCheck(t *testing.T) {
	kb := kbTreeChdir(t)
	suite := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	writeSuite(t, suite)

	// No receipt yet → STALE → exit 1.
	if code := evalGateCheck([]string{"-suite", suite}); code != gateExitFail {
		t.Errorf("gate-check (no receipt) = %d, want %d (STALE)", code, gateExitFail)
	}

	// Audited override → exit 0.
	t.Setenv("OLIFANT_EVAL_GATE_SKIP", "emergency hotfix")
	if code := evalGateCheck([]string{"-suite", suite}); code != gateExitPass {
		t.Errorf("gate-check (override) = %d, want %d", code, gateExitPass)
	}
}

func TestEvalGateCheck_KBNotFoundIsUsage(t *testing.T) {
	// chdir into a bare temp dir outside the platform tree → findUp fails →
	// kb-root not found → usage exit 2.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if code := evalGateCheck(nil); code != gateExitUsage {
		t.Errorf("gate-check (no kb) = %d, want %d", code, gateExitUsage)
	}
}

// ===== eval gate (full run) =====

func TestEvalGate_PassPath(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON) // live fakes → preflight passes, run produces a clean VALID case
	suite := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	writeSuite(t, suite)

	code := evalGate([]string{
		"-suite", suite,
		"-min-clean", "1",
		"-max-blockers", "5",
		"-min-first-try", "0",
		"-notify", // exercises driftLog (HOME redirected)
	})
	if code != gateExitPass {
		t.Errorf("eval gate = %d, want %d (PASS)", code, gateExitPass)
	}
}

func TestEvalGate_SkippedWhenDepsDown(t *testing.T) {
	kb := kbTreeChdir(t)
	// Point endpoints at a closed server → preflight reports deps-down → SKIPPED.
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	t.Setenv("OLIFANT_OLLAMA_URL", "http://127.0.0.1:1")
	t.Setenv("OLIFANT_CHROMA_URL", "http://127.0.0.1:1")
	suite := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	writeSuite(t, suite)

	code := evalGate([]string{"-suite", suite, "-notify"})
	if code != gateExitSkipped {
		t.Errorf("eval gate (deps down) = %d, want %d (SKIPPED)", code, gateExitSkipped)
	}
}
