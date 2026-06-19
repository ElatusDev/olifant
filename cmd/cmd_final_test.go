package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/history"
	"github.com/ElatusDev/olifant/internal/embedder"
)

func TestLanguageHintForPath_Full(t *testing.T) {
	cases := map[string]string{
		"a.java": "java", "a.kt": "kotlin", "a.kts": "kotlin", "a.ts": "typescript",
		"a.tsx": "tsx", "a.js": "javascript", "a.jsx": "jsx", "a.go": "go",
		"a.py": "python", "a.rb": "ruby", "a.swift": "swift", "a.rs": "rust",
		"a.tf": "terraform", "a.sql": "sql", "a.yaml": "yaml", "a.json": "json",
		"a.xml": "xml", "a.sh": "shell", "a.unknown": "",
	}
	for path, want := range cases {
		if got := languageHintForPath(path); got != want {
			t.Errorf("languageHintForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRecallOllama(t *testing.T) {
	fakeStack(t, "") // env-injected fake Ollama /api/embed
	queries := []embedder.Query{
		{ID: "q1", Text: "tenant scoping", ExpectedSource: "a.md"},
	}
	sents := []embedder.Sentence{
		{ID: "s1", Text: "tenant id is stamped", Source: "a.md"},
		{ID: "s2", Text: "composite key pattern", Source: "b.md"},
	}
	results, elapsed := recallOllama(queries, sents, 5, 32, "bge-m3")
	if len(results) != 1 {
		t.Fatalf("recallOllama returned %d results, want 1", len(results))
	}
	if elapsed < 0 {
		t.Errorf("elapsed = %d", elapsed)
	}
	if len(results[0].Hits) == 0 {
		t.Error("expected at least one hit")
	}
}

func TestEmbedderLs(t *testing.T) {
	// Swap the modal runner for a no-op `true` so no real `modal` is invoked.
	swapRunner(t, func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	if code := embedderLs(nil); code != 0 {
		t.Errorf("embedderLs = %d, want 0", code)
	}
	// Through the dispatcher too.
	if code := Embedder([]string{"ls"}); code != 0 {
		t.Errorf("Embedder(ls) = %d, want 0", code)
	}
}

func TestHistoryStats_PopulatedManifest(t *testing.T) {
	dir := t.TempDir()
	man := filepath.Join(dir, "history-manifest.yaml")
	m := &history.Manifest{BuilderVersion: "v1"}
	m.UpdateRepo("core-api", "backend", "abc1234", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		history.RunDelta{CommitsAdded: 5, SnapshotsAdded: 9})
	if err := history.SaveManifest(man, m); err != nil {
		t.Fatal(err)
	}

	_, cURL := indexServers(t) // chroma heartbeat + count probe
	if code := historyStats([]string{"-manifest", man, "-chroma-url", cURL}); code != 0 {
		t.Errorf("historyStats (populated) = %d, want 0", code)
	}
}

func TestValidate_ClaimFileAndUsage(t *testing.T) {
	// Missing diff → usage exit 2.
	if code := Validate([]string{"-claim-text", "x"}); code != 2 {
		t.Errorf("Validate(no diff) = %d, want 2", code)
	}
	// Missing claim → usage exit 2.
	if code := Validate([]string{"-diff", filepath.Join(t.TempDir(), "p.diff")}); code != 2 {
		t.Errorf("Validate(no claim) = %d, want 2", code)
	}

	// Claim-from-file path through the full pipeline.
	fakeStack(t, validateJSON)
	dir := t.TempDir()
	claim := filepath.Join(dir, "claim.txt")
	_ = os.WriteFile(claim, []byte("added a test"), 0o644)
	patch := filepath.Join(dir, "c.diff")
	_ = os.WriteFile(patch, []byte("diff --git a/x b/x\n+line\n"), 0o644)
	if code := Validate([]string{"-no-record", "-no-retrieval", "-claim", claim, "-diff", patch}); code != 0 {
		t.Errorf("Validate(claim-file) = %d, want 0", code)
	}
}

func TestChallenge_FileInput(t *testing.T) {
	fakeStack(t, challengeJSON)
	dir := t.TempDir()
	src := filepath.Join(dir, "Foo.java")
	_ = os.WriteFile(src, []byte("class Foo {}\n"), 0o644)
	if code := Challenge([]string{"-no-record", "-file", src}); code != 0 {
		t.Errorf("Challenge(-file) = %d, want 0", code)
	}
	// Neither request nor file → usage exit 2.
	if code := Challenge([]string{"-no-record"}); code != 2 {
		t.Errorf("Challenge(no input) = %d, want 2", code)
	}
}

func TestDispatchArms(t *testing.T) {
	// Route through the dispatchers' valid subcommand arms (stub/usage paths)
	// to cover the switch routing itself.
	if code := Corpus([]string{"stats"}); code != 1 { // stub
		t.Errorf("Corpus(stats) = %d, want 1", code)
	}
	if code := Plan([]string{"stats"}); code != 1 { // not implemented
		t.Errorf("Plan(stats) = %d, want 1", code)
	}
	if code := Dataset([]string{"stats"}); code != 2 && code != 1 {
		t.Errorf("Dataset(stats) = %d, want 1 or 2", code)
	}
}
