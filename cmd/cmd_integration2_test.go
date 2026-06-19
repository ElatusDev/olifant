package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRepoWithCommits(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE=2026-03-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-03-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	_ = os.WriteFile(filepath.Join(dir, "Foo.java"), []byte("class Foo {}\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "initial")
	_ = os.WriteFile(filepath.Join(dir, "Foo.java"), []byte("class Foo { int x; }\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "feat: add field")
}

func TestCorpusIndexV2_Integration(t *testing.T) {
	fakeStack(t, "")
	kb := t.TempDir()
	root := filepath.Join(kb, "corpus", "v2-curriculum")
	vocab := filepath.Join(root, "vocab", "core-api")
	prose := filepath.Join(root, "prose", "core-api")
	_ = os.MkdirAll(vocab, 0o755)
	_ = os.MkdirAll(prose, 0o755)
	_ = os.WriteFile(filepath.Join(vocab, "s.yaml"),
		[]byte("- id: s1\n  text: TenantScoped\n  source: Foo.java\n  line: 4\n"), 0o644)
	_ = os.WriteFile(filepath.Join(prose, "p.yaml"),
		[]byte("- id: p1\n  text: tenant id is stamped\n  source: d.md\n  line: 1\n"), 0o644)

	if code := corpusIndexV2([]string{"-kb-root", kb}); code != 0 {
		t.Errorf("corpusIndexV2 = %d, want 0", code)
	}
}

func TestRepoIngest_Integration(t *testing.T) {
	fakeStack(t, "")
	platform := t.TempDir()
	gitRepoWithCommits(t, mkdir(t, filepath.Join(platform, "core-api")))
	kb := mkdir(t, filepath.Join(platform, "knowledge-base"))

	code := repoIngest([]string{
		"-platform-root", platform,
		"-kb-root", kb,
		"-out", filepath.Join(platform, "out"),
		"-repo", "core-api",
	})
	if code != 0 {
		t.Errorf("repoIngest = %d, want 0", code)
	}
}

func TestHistoryScan_FullWithRepo(t *testing.T) {
	platform := t.TempDir()
	gitRepoWithCommits(t, mkdir(t, filepath.Join(platform, "core-api")))
	kb := mkdir(t, filepath.Join(platform, "knowledge-base"))

	code := historyScan([]string{
		"-platform-root", platform,
		"-kb-root", kb,
		"-out", filepath.Join(platform, "out"),
		"-repo", "core-api",
		"-manifest", filepath.Join(platform, "m.yaml"),
	})
	if code != 0 {
		t.Errorf("historyScan (full) = %d, want 0", code)
	}
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
