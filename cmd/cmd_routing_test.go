package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvalGate_FailPath(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	suite := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	writeSuite(t, suite)

	// An impossible clean threshold forces FAIL → exercises the reasons +
	// DiffTable + FirstAttemptBlockerReport printing branch.
	code := evalGate([]string{"-suite", suite, "-min-clean", "100", "-min-first-try", "0"})
	if code != gateExitFail {
		t.Errorf("eval gate (forced fail) = %d, want %d", code, gateExitFail)
	}
}

func TestDatasetBuild_SuccessWithDecisions(t *testing.T) {
	kb := t.TempDir()
	dec := filepath.Join(kb, "decisions", "log.yaml")
	if err := os.MkdirAll(filepath.Dir(dec), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "decisions:\n" +
		"  - id: D1\n    title: Use composite keys\n    decision: composite keys for tenant scoped entities\n    rationale: avoids cross-tenant leakage\n    source: architecture/keys.md\n"
	if err := os.WriteFile(dec, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	code := datasetBuild([]string{"-kb-root", kb, "-out", out, "-sources", "decisions", "-v"})
	if code != 0 {
		t.Errorf("datasetBuild (decisions) = %d, want 0", code)
	}
}

// TestDispatchRouting routes valid subcommands through the top-level
// dispatchers to cover the switch arms themselves.
func TestDispatchRouting(t *testing.T) {
	// Dataset → pack
	in := t.TempDir()
	_ = os.WriteFile(filepath.Join(in, "a.jsonl"), []byte(`{"k":1}`+"\n"), 0o644)
	if code := Dataset([]string{"pack", "-in", in, "-out", filepath.Join(t.TempDir(), "p.jsonl")}); code != 0 {
		t.Errorf("Dataset(pack) = %d, want 0", code)
	}
	// Dataset → sanitize-docs
	docs := t.TempDir()
	_ = os.WriteFile(filepath.Join(docs, "a.md"), []byte("# d\n\nx\n"), 0o644)
	if code := Dataset([]string{"sanitize-docs", "-root", docs, "-dry-run"}); code != 0 {
		t.Errorf("Dataset(sanitize-docs) = %d, want 0", code)
	}

	// History → stats
	man := filepath.Join(t.TempDir(), "m.yaml")
	_ = os.WriteFile(man, []byte("builder_version: v1\nrepos: []\n"), 0o644)
	if code := History([]string{"stats", "-manifest", man}); code != 0 {
		t.Errorf("History(stats) = %d, want 0", code)
	}

	// Plan → validate
	planDir := t.TempDir()
	plan := filepath.Join(planDir, "p.yaml")
	_ = os.WriteFile(plan, []byte("plan_id: p1\ngoal: g\nsteps:\n  - id: step_01\n    description: d\n"), 0o644)
	if code := Plan([]string{"validate", plan}); code != 0 {
		t.Errorf("Plan(validate) = %d, want 0", code)
	}

	// Corpus → scan
	repo := t.TempDir()
	src := filepath.Join(repo, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "Foo.java"), []byte("package x;\nclass Foo {}\n"), 0o644)
	if code := Corpus([]string{"scan", "-repo", "core-api", "-repo-root", repo, "-module", "m", "-source-root", src, "-dry-run"}); code != 0 {
		t.Errorf("Corpus(scan) = %d, want 0", code)
	}
}
