package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify_Errors(t *testing.T) {
	if _, err := Classify(ClassifyConfig{}); err == nil {
		t.Error("empty InputPath should error")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(bad, []byte("not: [valid"), 0o644)
	if _, err := Classify(ClassifyConfig{InputPath: bad}); err == nil {
		t.Error("invalid yaml should error")
	}
}

func TestClassify_DryRunCountsBatches(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prose.yaml")
	// 3 sentences, one already classified → 2 queued.
	_ = os.WriteFile(p, []byte(`- id: s1
  text: first
- id: s2
  text: second
- id: s3
  text: third
  tags:
    semantic_role: definition
`), 0o644)
	stats, err := Classify(ClassifyConfig{InputPath: p, BatchSize: 1, DryRun: true})
	if err != nil {
		t.Fatalf("Classify dry-run: %v", err)
	}
	if stats.InputSentences != 3 {
		t.Errorf("InputSentences = %d, want 3", stats.InputSentences)
	}
	if stats.BatchesAttempted != 2 {
		t.Errorf("BatchesAttempted = %d, want 2 (one already classified)", stats.BatchesAttempted)
	}
}

func TestClassify_BatchExecFailureIsRecorded(t *testing.T) {
	// Force `claude` to be unresolvable so classifyBatch's exec path fails
	// deterministically (no real subprocess, no network).
	t.Setenv("PATH", t.TempDir())

	p := filepath.Join(t.TempDir(), "prose.yaml")
	_ = os.WriteFile(p, []byte("- id: s1\n  text: a sentence to classify\n"), 0o644)

	stats, err := Classify(ClassifyConfig{InputPath: p, BatchSize: 50})
	if err != nil {
		t.Fatalf("Classify should not hard-error on batch failure: %v", err)
	}
	if stats.BatchesAttempted != 1 || stats.BatchesFailed != 1 {
		t.Errorf("expected 1 attempted / 1 failed, got attempted=%d failed=%d", stats.BatchesAttempted, stats.BatchesFailed)
	}
	if stats.ClassifiedCount != 0 {
		t.Errorf("ClassifiedCount = %d, want 0 (batch failed)", stats.ClassifiedCount)
	}
}

func TestProse_Errors(t *testing.T) {
	if _, err := Prose(ScanConfig{}); err == nil {
		t.Error("missing RepoRoot should error")
	}
	if _, err := Prose(ScanConfig{RepoRoot: "/r"}); err == nil {
		t.Error("missing SourceRoot should error")
	}
	if _, err := Prose(ScanConfig{RepoRoot: "/r", SourceRoot: "/r/src"}); err == nil {
		t.Error("missing OutPath (non-dry-run) should error")
	}
}

func TestProse_ExtractsAndWrites(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "docs")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "# Heading\n\nThe tenant id MUST be stamped automatically. This is a constraint.\n\nUse the composite key pattern for entities.\n"
	if err := os.WriteFile(filepath.Join(src, "guide.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "prose.yaml")

	stats, err := Prose(ScanConfig{
		Repo:       "core-api",
		RepoRoot:   repo,
		SourceRoot: src,
		OutPath:    out,
	})
	if err != nil {
		t.Fatalf("Prose: %v", err)
	}
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", stats.FilesScanned)
	}
	if stats.SymbolsEmitted == 0 {
		t.Error("expected sentences extracted")
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("prose YAML not written: %v", err)
	}
}

func TestProse_DryRunSkipsWrite(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "docs")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "g.md"), []byte("# T\n\nA sentence here is fine.\n"), 0o644)

	stats, err := Prose(ScanConfig{Repo: "core-api", RepoRoot: repo, SourceRoot: src, DryRun: true})
	if err != nil {
		t.Fatalf("Prose dry-run: %v", err)
	}
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", stats.FilesScanned)
	}
}

func TestProse_KBFilterDropsNonCuratedDirs(t *testing.T) {
	repo := t.TempDir()
	// A KB repo .md outside the curated prefixes is filtered out.
	src := filepath.Join(repo, "scratch")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "notes.md"), []byte("# x\n\nsome text.\n"), 0o644)

	stats, err := Prose(ScanConfig{Repo: "knowledge-base", RepoRoot: repo, SourceRoot: src, DryRun: true})
	if err != nil {
		t.Fatalf("Prose: %v", err)
	}
	if stats.FilesScanned != 0 {
		t.Errorf("KB scratch dir should be filtered, FilesScanned = %d", stats.FilesScanned)
	}
	_ = strings.TrimSpace("")
}
