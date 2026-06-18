package validate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsExistingFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isExistingFile(f) {
		t.Errorf("isExistingFile(%q) = false, want true", f)
	}
	if isExistingFile(filepath.Join(dir, "missing.txt")) {
		t.Errorf("isExistingFile(missing) = true, want false")
	}
	if isExistingFile(dir) {
		t.Errorf("isExistingFile(dir) = true, want false (directories are not files)")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(f, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFile(f)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "payload" {
		t.Errorf("readFile = %q, want payload", got)
	}
	if _, err := readFile(filepath.Join(dir, "missing.txt")); err == nil {
		t.Error("readFile(missing) should error")
	}
}
