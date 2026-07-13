package dataset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatestSource_NoDirIsEmpty(t *testing.T) {
	p, n, err := LatestSource(t.TempDir())
	if p != "" || n != 0 || err != nil {
		t.Errorf("LatestSource(empty kb) = %q, %d, %v", p, n, err)
	}
}

func TestLatestSource_CountsEntriesOfLatest(t *testing.T) {
	kb := t.TempDir()
	dir := filepath.Join(kb, "eval", "failure-modes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v1 := "meta:\n  version: 1\nentries:\n  - id: a\n  - id: b\n"
	v2 := "meta:\n  version: 2\nentries:\n  - id: a\n  - id: b\n  - id: c\n"
	if err := os.WriteFile(filepath.Join(dir, "v1.yaml"), []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v2.yaml"), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	p, n, err := LatestSource(kb)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "v2.yaml" || n != 3 {
		t.Errorf("LatestSource = %q, %d; want v2.yaml, 3", p, n)
	}
}
