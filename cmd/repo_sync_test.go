package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/history"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/repos"
)

// platformFixture builds a temp platform tree: knowledge-base/ (returned by
// kbTreeChdir-style layout) + all 7 DefaultRepos as real git repos with one
// committed file each. Returns (kbRoot, platformRoot).
func platformFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLIFANT_KB_ROOT", "")
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(filepath.Join(kb, "corpus", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "README.md"), []byte("# KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, rs := range repos.DefaultRepos(root) {
		if err := os.MkdirAll(rs.Path, 0o755); err != nil {
			t.Fatal(err)
		}
		git(rs.Path, "init")
		git(rs.Path, "config", "user.email", "t@t")
		git(rs.Path, "config", "user.name", "t")
		if err := os.WriteFile(filepath.Join(rs.Path, "main.go"), []byte("package "+rs.Name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(rs.Path, "add", "-A")
		git(rs.Path, "commit", "-m", "init", "--no-gpg-sign")
	}

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(kb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return kb, root
}

func headSHAOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:40])
}

func landRepoManifest(t *testing.T, kb, platform string) {
	t.Helper()
	scoped, _, err := repos.CollectChunks(repos.IngestConfig{Repos: repos.DefaultRepos(platform)})
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteManifest(repos.ManifestPath(kb), repos.BuildManifest(scoped)); err != nil {
		t.Fatal(err)
	}
}

func TestRepoSyncVerb_MissingManifestFails(t *testing.T) {
	kb, _ := platformFixture(t)
	if code := Repo([]string{"sync", "-kb-root", kb}); code != 1 {
		t.Errorf("repo sync (no manifest) = %d, want 1", code)
	}
}

func TestRepoSyncVerb_DryRunReportsDrift(t *testing.T) {
	kb, platform := platformFixture(t)
	landRepoManifest(t, kb, platform)

	// No drift: dry-run is a no-op.
	if code := Repo([]string{"sync", "-kb-root", kb, "-platform-root", platform, "-dry-run"}); code != 0 {
		t.Fatalf("repo sync -dry-run (clean) = %d, want 0", code)
	}

	// Drift: stage a new file; dry-run reports without writing.
	repo := repos.DefaultRepos(platform)[0]
	if err := os.WriteFile(filepath.Join(repo.Path, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "-C", repo.Path, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	before, _ := os.ReadFile(repos.ManifestPath(kb))
	if code := Repo([]string{"sync", "-kb-root", kb, "-platform-root", platform, "-dry-run"}); code != 0 {
		t.Fatalf("repo sync -dry-run (drift) = %d, want 0", code)
	}
	after, _ := os.ReadFile(repos.ManifestPath(kb))
	if string(before) != string(after) {
		t.Error("dry-run mutated the landed repo manifest")
	}
}

func TestFamilyStatus_NotMintedDrifts(t *testing.T) {
	kb, platform := platformFixture(t)
	if !familyStatus(kb, platform) {
		t.Error("familyStatus with no repo manifest should report drift")
	}
}

func TestFamilyStatus_CleanThenPendingHistory(t *testing.T) {
	kb, platform := platformFixture(t)
	landRepoManifest(t, kb, platform)

	// History cursor at each repo's HEAD → zero pending.
	hm := &history.Manifest{LastRunAt: time.Now().UTC().Format(time.RFC3339)}
	for _, rs := range repos.DefaultRepos(platform) {
		hm.UpdateRepo(rs.Name, rs.Scope, headSHAOf(t, rs.Path), time.Now(), history.RunDelta{})
	}
	if err := history.SaveManifest(filepath.Join(kb, "short-term", "history-manifest.yaml"), hm); err != nil {
		t.Fatal(err)
	}
	if familyStatus(kb, platform) {
		t.Error("clean families reported drift")
	}

	// A new commit → pending history → drift.
	repo := repos.DefaultRepos(platform)[0]
	if err := os.WriteFile(filepath.Join(repo.Path, "later.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "later", "--no-gpg-sign"}} {
		cmd := exec.Command("git", append([]string{"-C", repo.Path}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// The new commit ALSO changes the code diff (new staged file) — both
	// signals point the same way; assert the aggregate.
	if !familyStatus(kb, platform) {
		t.Error("pending history commit not reported as drift")
	}
}

func TestCorpusStatus_FamiliesFlagWiresIn(t *testing.T) {
	kb, platform := platformFixture(t)
	// Corpus half: land a corpus manifest via build (tiny tree).
	if code := Corpus([]string{"build", "-kb-root", kb, "-platform-root", platform, "-memory-root", t.TempDir()}); code != 0 {
		t.Fatalf("corpus build failed")
	}
	// Corpus clean, but families drifted (repo manifest not minted) → exit 1.
	// (platformRoot comes from findUp — cwd sits inside the fixture tree.)
	if code := Corpus([]string{"status", "-kb-root", kb, "-memory-root", t.TempDir(), "-families"}); code != 1 {
		t.Errorf("status -families (unminted code family) = %d, want 1", code)
	}
	// Default (no -families) unchanged → exit 0.
	if code := Corpus([]string{"status", "-kb-root", kb, "-memory-root", t.TempDir()}); code != 0 {
		t.Errorf("status without -families = %d, want 0 (back-compat)", code)
	}
}
