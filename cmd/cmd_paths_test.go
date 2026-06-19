package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusScan_WritesOutput(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "Foo.java"), []byte("package x;\npublic class Foo {}\n"), 0o644)
	out := filepath.Join(t.TempDir(), "symbols.yaml")
	code := corpusScan([]string{
		"-repo", "core-api", "-repo-root", repo, "-module", "m", "-source-root", src, "-out", out, "-v",
	})
	if code != 0 {
		t.Errorf("corpusScan = %d, want 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("scan output not written: %v", err)
	}
}

func TestCorpusProse_WritesOutput(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "docs")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "g.md"), []byte("# T\n\nThe tenant id MUST be stamped.\n"), 0o644)
	out := filepath.Join(t.TempDir(), "prose.yaml")
	code := corpusProse([]string{
		"-repo", "core-api", "-repo-root", repo, "-source-root", src, "-out", out, "-v",
	})
	if code != 0 {
		t.Errorf("corpusProse = %d, want 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("prose output not written: %v", err)
	}
}

// The synth commands write a short-term ledger turn unless --no-record. Running
// them inside a chdir'd KB tree (with verbose) exercises the record path.

func TestPromptBuild_RecordAndVerbose(t *testing.T) {
	kbTreeChdir(t)
	fakeStack(t, planJSON)
	out := t.TempDir()
	if code := promptBuild([]string{"-v", "-out", out, "add a tenant scoped invoice entity"}); code != 0 {
		t.Errorf("promptBuild (record+verbose) = %d, want 0", code)
	}
}

func TestChallenge_RecordAndVerbose(t *testing.T) {
	kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	if code := Challenge([]string{"-v", "add a tenant scoped invoice entity"}); code != 0 {
		t.Errorf("Challenge (record+verbose) = %d, want 0", code)
	}
}

func TestValidate_RecordAndVerbose(t *testing.T) {
	kbTreeChdir(t)
	fakeStack(t, validateJSON)
	patch := filepath.Join(t.TempDir(), "p.diff")
	_ = os.WriteFile(patch, []byte("diff --git a/x b/x\n+l\n"), 0o644)
	if code := Validate([]string{"-v", "-no-retrieval", "-claim-text", "added a test", "-diff", patch}); code != 0 {
		t.Errorf("Validate (record+verbose) = %d, want 0", code)
	}
}
