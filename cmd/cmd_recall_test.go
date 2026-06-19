package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbedderRecall_FullPair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeStack(t, "") // baseline recallOllama embeds via fake Ollama
	// Modal candidate returns the recall envelope.
	payload := `===OLIFANT_RECALL_JSON===
{"queries":[{"query_id":"q01","hits":[{"sentence_id":"s1","source":"a.md","score":0.9}]}]}
===END_OLIFANT_RECALL_JSON===`
	stub := payloadStub(t, payload)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	prose := t.TempDir()
	_ = os.WriteFile(filepath.Join(prose, "s.yaml"),
		[]byte("- id: s1\n  text: tenant id is stamped\n  source: a.md\n  line: 1\n"), 0o644)
	suite := filepath.Join(t.TempDir(), "q.yaml")
	_ = os.WriteFile(suite, []byte("suite_id: r\nqueries:\n  - id: q01\n    scope: backend\n    text: where stamped\n    expected_source: a.md\n"), 0o644)
	out := filepath.Join(t.TempDir(), "report.json")

	code := embedderRecall([]string{"-queries", suite, "-prose-dir", prose, "-out", out})
	if code != 0 {
		t.Errorf("embedderRecall (full pair) = %d, want 0", code)
	}
}

func TestCorpusDispatchArms(t *testing.T) {
	// classify arm
	prose := filepath.Join(t.TempDir(), "prose.yaml")
	_ = os.WriteFile(prose, []byte("- id: s1\n  text: a sentence\n"), 0o644)
	if code := Corpus([]string{"classify", "-input", prose, "-dry-run"}); code != 0 {
		t.Errorf("Corpus(classify) = %d, want 0", code)
	}
	// prose arm
	repo := t.TempDir()
	src := filepath.Join(repo, "docs")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "g.md"), []byte("# T\n\nThe id MUST be set.\n"), 0o644)
	if code := Corpus([]string{"prose", "-repo", "core-api", "-repo-root", repo, "-source-root", src, "-dry-run"}); code != 0 {
		t.Errorf("Corpus(prose) = %d, want 0", code)
	}
	// build arm
	kb := t.TempDir()
	_ = os.MkdirAll(filepath.Join(kb, "patterns"), 0o755)
	_ = os.WriteFile(filepath.Join(kb, "patterns", "b.md"), []byte("# P\n\n## S\n\nbody\n"), 0o644)
	if code := Corpus([]string{"build", "-kb-root", kb, "-platform-root", kb, "-memory-root", t.TempDir(), "-out", filepath.Join(kb, "out")}); code != 0 {
		t.Errorf("Corpus(build) = %d, want 0", code)
	}
}
