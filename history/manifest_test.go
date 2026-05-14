package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadManifest_missingFileReturnsEmpty(t *testing.T) {
	m, err := LoadManifest(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if m == nil {
		t.Fatal("expected empty manifest, got nil")
	}
	if len(m.Repos) != 0 {
		t.Errorf("expected zero repos, got %d", len(m.Repos))
	}
	if m.BuilderVersion == "" {
		t.Errorf("expected BuilderVersion to be set")
	}
}

func TestSaveAndLoad_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	in := &Manifest{
		BuilderVersion: BuilderVersion,
		LastRunAt:      "2026-05-14T17:00:00Z",
		SinceFloor:     "2026-01-01",
	}
	committedAt := time.Date(2026, 5, 8, 18, 28, 40, 0, time.UTC)
	in.UpdateRepo("infra", "infra", "fedc4adcd5f54ad15cd521b107b421990cb43b90", committedAt,
		RunDelta{CommitsAdded: 27, SnapshotsAdded: 186})
	in.UpdateRepo("core-api", "backend", "deadbeef", committedAt,
		RunDelta{CommitsAdded: 100, SnapshotsAdded: 800})

	if err := SaveManifest(path, in); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	out, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if out.BuilderVersion != in.BuilderVersion {
		t.Errorf("builder version: %q vs %q", out.BuilderVersion, in.BuilderVersion)
	}
	if len(out.Repos) != 2 {
		t.Fatalf("repos: got %d, want 2", len(out.Repos))
	}
	// After SaveManifest sorts alphabetically: core-api before infra.
	if out.Repos[0].Name != "core-api" {
		t.Errorf("repo[0] = %q, want core-api (alphabetical)", out.Repos[0].Name)
	}
	if out.LastSHA("infra") != "fedc4adcd5f54ad15cd521b107b421990cb43b90" {
		t.Errorf("infra last_sha: %q", out.LastSHA("infra"))
	}
	if out.LastSHA("unknown") != "" {
		t.Errorf("unknown repo should return empty, got %q", out.LastSHA("unknown"))
	}
}

func TestUpdateRepo_overwritesExisting(t *testing.T) {
	m := &Manifest{}
	now := time.Now()
	m.UpdateRepo("infra", "infra", "sha1", now, RunDelta{CommitsAdded: 5})
	m.UpdateRepo("infra", "infra", "sha2", now, RunDelta{CommitsAdded: 10})
	if len(m.Repos) != 1 {
		t.Errorf("expected 1 repo after overwrite, got %d", len(m.Repos))
	}
	if m.LastSHA("infra") != "sha2" {
		t.Errorf("expected sha2 after overwrite, got %q", m.LastSHA("infra"))
	}
	if m.Repos[0].LastRun.CommitsAdded != 10 {
		t.Errorf("expected last-run delta from second update, got %+v", m.Repos[0].LastRun)
	}
}

func TestSaveManifest_atomicWrite(t *testing.T) {
	// After a successful save, neither the .tmp file nor a corrupted
	// final file should be left behind.
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := SaveManifest(path, &Manifest{BuilderVersion: BuilderVersion}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); err == nil {
		t.Errorf("temp file still exists at %s", tmp)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !strings.Contains(string(body), "builder_version") {
		t.Errorf("final file missing expected content: %q", body)
	}
}
