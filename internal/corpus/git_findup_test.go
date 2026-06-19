package corpus

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitLsFilesSHAs(t *testing.T) {
	// Non-git dir → empty map, no error.
	m, err := gitLsFilesSHAs(t.TempDir())
	if err != nil {
		t.Fatalf("non-git dir errored: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("non-git dir returned %d entries, want 0", len(m))
	}

	// Real git repo with a staged file → path→sha recorded.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")

	got, err := gitLsFilesSHAs(dir)
	if err != nil {
		t.Fatalf("gitLsFilesSHAs: %v", err)
	}
	if sha, ok := got["a.md"]; !ok || sha == "" {
		t.Errorf("expected a.md → sha, got %v", got)
	}
}

func TestFindUp(t *testing.T) {
	root := t.TempDir()
	// Create knowledge-base/README.md at the root, then chdir into a nested
	// subdir so findUp must walk upward to locate it.
	kbReadme := filepath.Join(root, "knowledge-base", "README.md")
	if err := os.MkdirAll(filepath.Dir(kbReadme), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kbReadme, []byte("# KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		t.Fatal("findUp did not locate knowledge-base/README.md walking up")
	}
	// Resolve symlinks (macOS /var → /private/var) before comparing.
	gotReal, _ := filepath.EvalSymlinks(found)
	wantReal, _ := filepath.EvalSymlinks(kbReadme)
	if gotReal != wantReal {
		t.Errorf("findUp = %q, want %q", gotReal, wantReal)
	}
}
