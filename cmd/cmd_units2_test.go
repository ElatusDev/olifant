package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatasetPack(t *testing.T) {
	// Missing required flags → exit 2.
	if code := datasetPack(nil); code != 2 {
		t.Errorf("datasetPack(nil) = %d, want 2", code)
	}

	in := t.TempDir()
	_ = os.WriteFile(filepath.Join(in, "a.jsonl"), []byte(`{"k":1}`+"\n"), 0o644)
	out := filepath.Join(t.TempDir(), "packed.jsonl")
	if code := datasetPack([]string{"-in", in, "-out", out}); code != 0 {
		t.Errorf("datasetPack happy = %d, want 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("packed output not written: %v", err)
	}
}

func TestDatasetStats(t *testing.T) {
	if code := datasetStats(nil); code != 2 {
		t.Errorf("datasetStats(nil) = %d, want 2", code)
	}
	// Missing manifest → exit 1.
	if code := datasetStats([]string{"-out", t.TempDir()}); code != 1 {
		t.Errorf("datasetStats(no-manifest) = %d, want 1", code)
	}
	// Present manifest → exit 0.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "manifest.yaml"),
		[]byte("run_id: r1\nbuilder_version: v1\ntotals:\n  examples_emitted: 3\n"), 0o644)
	if code := datasetStats([]string{"-out", dir}); code != 0 {
		t.Errorf("datasetStats(good) = %d, want 0", code)
	}
}

// turnTreeChdir builds a KB tree with short-term/turns/*.yaml and chdirs into a
// nested dir so turnsDir()'s findUp resolves.
func turnTreeChdir(t *testing.T) (turnID string) {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(kb, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "README.md"), []byte("# KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	turns := filepath.Join(kb, "short-term", "turns")
	if err := os.MkdirAll(turns, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "2026-06-19T08-05-03Z-abc123"
	if err := os.WriteFile(filepath.Join(turns, id+".yaml"),
		[]byte("turn_id: "+id+"\nsubcommand: challenge\nrequest: do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(kb, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	return id
}

func TestTurnCommands(t *testing.T) {
	id := turnTreeChdir(t)

	if code := Turn([]string{"list"}); code != 0 {
		t.Errorf("turn list = %d, want 0", code)
	}
	if code := Turn([]string{"stats"}); code != 0 {
		t.Errorf("turn stats = %d, want 0", code)
	}
	if code := Turn([]string{"show", id}); code != 0 {
		t.Errorf("turn show = %d, want 0", code)
	}
	// show with no id → usage exit 2.
	if code := Turn([]string{"show"}); code != 2 {
		t.Errorf("turn show (no id) = %d, want 2", code)
	}
}
