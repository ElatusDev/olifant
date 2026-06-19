package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "- id: s1\n  text: the tenant id is stamped automatically on insert\n  source: a.md\n  line: 1\n" +
		"- id: s2\n  text: use the composite key pattern for tenant scoped entities\n  source: b.md\n  line: 2\n"
	if err := os.WriteFile(filepath.Join(dir, "sentences.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDatasetEmbedderTriples_MiningOnly(t *testing.T) {
	prose := writeProseDir(t)
	// --mining-only loads + mines + prints stats; no Opus/claude subprocess.
	code := datasetEmbedderTriples([]string{"-prose-dir", prose, "-mining-only"})
	if code != 0 {
		t.Errorf("datasetEmbedderTriples --mining-only = %d, want 0", code)
	}
	// Missing prose-dir/kb-root, chdir'd outside the platform tree so autodetect
	// also fails → exit 1.
	t.Chdir(t.TempDir())
	if code := datasetEmbedderTriples([]string{"-mining-only"}); code != 1 {
		t.Errorf("datasetEmbedderTriples (no prose) = %d, want 1", code)
	}
}

func TestEmbedderRecall_OllamaCandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeStack(t, "") // env-injected fake Ollama /api/embed for recallOllama
	prose := writeProseDir(t)

	suite := filepath.Join(t.TempDir(), "queries.yaml")
	body := "suite_id: recall-smoke\nqueries:\n" +
		"  - id: q1\n    scope: backend\n    text: how is tenant id stamped\n    expected_source: a.md\n"
	if err := os.WriteFile(suite, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "recall-report.json")

	// --ollama-candidate evaluates an Ollama-served model and skips the Modal path.
	code := embedderRecall([]string{
		"-queries", suite,
		"-prose-dir", prose,
		"-ollama-candidate", "bge-m3",
		"-out", out,
	})
	if code != 0 {
		t.Errorf("embedderRecall (ollama candidate) = %d, want 0", code)
	}
}
