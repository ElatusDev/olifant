package history

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeHistoryRepo builds a temp git repo with two commits so Walk/Parse/Scan
// have real diff signal to chew on.
func makeHistoryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init")
	write("Foo.java", "class Foo {}\n")
	run("add", "-A")
	run("commit", "-m", "initial")
	write("Foo.java", "class Foo { int x; }\n")
	write("Bar.java", "class Bar {}\n")
	run("add", "-A")
	run("commit", "-m", "feat: add field and Bar")
	return dir
}

func TestScan_EndToEnd(t *testing.T) {
	repo := makeHistoryRepo(t)
	out := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "manifest.yaml")

	stats, err := Scan(context.Background(), ScanConfig{
		Repos:         []RepoSpec{{Name: "core-api", Path: repo, Scope: "backend"}},
		OutDir:        out,
		WriteJSONL:    true,
		ManifestPath:  manifest,
		WriteManifest: true,
		FullScan:      true,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Second commit has a parent → emitted; the initial commit is skipped.
	if stats.CommitsEmitted < 1 {
		t.Errorf("CommitsEmitted = %d, want >=1", stats.CommitsEmitted)
	}
	if stats.ReposProcessed != 1 {
		t.Errorf("ReposProcessed = %d, want 1", stats.ReposProcessed)
	}

	// JSONL families written.
	if _, err := os.Stat(filepath.Join(out, "core-api.commits.jsonl")); err != nil {
		t.Errorf("commits jsonl missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "core-api.snapshots.jsonl")); err != nil {
		t.Errorf("snapshots jsonl missing: %v", err)
	}

	// Manifest persisted and reloadable, recording the repo's last SHA.
	m, err := LoadManifest(manifest)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.LastSHA("core-api") == "" {
		t.Error("manifest did not record last_sha for core-api")
	}
}

func TestScan_SkipsMissingRepo(t *testing.T) {
	stats, err := Scan(context.Background(), ScanConfig{
		Repos:    []RepoSpec{{Name: "ghost", Path: "/no/such/repo", Scope: "backend"}},
		FullScan: true,
	})
	if err != nil {
		t.Fatalf("Scan missing repo should not error: %v", err)
	}
	if stats.ReposProcessed != 0 {
		t.Errorf("ReposProcessed = %d, want 0", stats.ReposProcessed)
	}
}

func TestScan_Incremental_StopsAtLastSHA(t *testing.T) {
	repo := makeHistoryRepo(t)
	manifest := filepath.Join(t.TempDir(), "m.yaml")

	// First full scan records the latest SHA.
	if _, err := Scan(context.Background(), ScanConfig{
		Repos:         []RepoSpec{{Name: "core-api", Path: repo, Scope: "backend"}},
		ManifestPath:  manifest,
		WriteManifest: true,
		FullScan:      true,
	}); err != nil {
		t.Fatal(err)
	}
	// Incremental re-scan: nothing new since last SHA → 0 emitted.
	stats, err := Scan(context.Background(), ScanConfig{
		Repos:         []RepoSpec{{Name: "core-api", Path: repo, Scope: "backend"}},
		ManifestPath:  manifest,
		WriteManifest: true,
	})
	if err != nil {
		t.Fatalf("incremental scan: %v", err)
	}
	if stats.CommitsEmitted != 0 {
		t.Errorf("incremental emitted = %d, want 0 (nothing new)", stats.CommitsEmitted)
	}
}

func TestDefaultRepos(t *testing.T) {
	specs := DefaultRepos("/plat")
	if len(specs) != 7 {
		t.Fatalf("DefaultRepos len = %d, want 7", len(specs))
	}
	if specs[0].Name != "infra" || specs[len(specs)-1].Name != "core-api" {
		t.Errorf("ordering = %q..%q", specs[0].Name, specs[len(specs)-1].Name)
	}
	if specs[len(specs)-1].Path != filepath.Join("/plat", "core-api") {
		t.Errorf("path = %q", specs[len(specs)-1].Path)
	}
}

func TestWalk_OpenError(t *testing.T) {
	// A non-git directory makes git.PlainOpen fail.
	_, _, err := Walk(context.Background(), t.TempDir(), "x", "backend", "", ScanConfig{})
	if err == nil || !strings.Contains(err.Error(), "git open") {
		t.Errorf("non-git dir should error on open, got %v", err)
	}
}
