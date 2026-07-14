package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitFixtureKB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// A knowledge-base/README.md marker so resolveRoots' findUp can anchor when
	// the flag points here, plus a decisions file for a resolvable cite.
	for rel, body := range map[string]string{
		"knowledge-base/README.md":        "kb\n",
		"knowledge-base/decisions/log.md": "### D210: x\n",
	} {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kb := filepath.Join(dir, "knowledge-base")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = kb
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "kb")
	return kb
}

func TestResolveKBTree_FS(t *testing.T) {
	kb := gitFixtureKB(t)
	tree, kbRoot, _, err := resolveKBTree(kb, "")
	if err != nil || tree == nil || kbRoot == "" {
		t.Fatalf("resolveKBTree(fs) = %v, %q, %v", tree, kbRoot, err)
	}
	if !tree.Exists("decisions/log.md") {
		t.Error("fs tree missing decisions/log.md")
	}
}

func TestResolveKBTree_GitRef(t *testing.T) {
	kb := gitFixtureKB(t)
	tree, _, _, err := resolveKBTree(kb, "HEAD")
	if err != nil || tree == nil {
		t.Fatalf("resolveKBTree(git HEAD) = %v, %v", tree, err)
	}
	if !tree.Exists("decisions/log.md") {
		t.Error("git tree missing decisions/log.md")
	}
}

func TestResolveKBTree_BadRef(t *testing.T) {
	kb := gitFixtureKB(t)
	_, _, _, err := resolveKBTree(kb, "no-such-ref")
	if err == nil {
		t.Error("resolveKBTree(bad ref) = nil error; want hard error (no silent fallback)")
	}
}

func TestPromptCheck_GitRef(t *testing.T) {
	kb := gitFixtureKB(t)
	doc := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(doc, []byte("This honors D210.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// D210 exists in the fixture ref → resolves → exit 0.
	if code := promptCheck([]string{"-git-ref", "HEAD", "-kb-root", kb, "-no-record", doc}); code != 0 {
		t.Errorf("prompt check -git-ref (resolvable) = %d; want 0", code)
	}
	// A bad ref is a hard setup error → exit 2.
	if code := promptCheck([]string{"-git-ref", "no-such-ref", "-kb-root", kb, "-no-record", doc}); code != 2 {
		t.Errorf("prompt check -git-ref (bad ref) = %d; want 2", code)
	}
}
