package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitJSONL_GroupsByScope(t *testing.T) {
	out := t.TempDir()
	examples := []Example{
		{Scope: "backend", Source: "a", Tier: 1},
		{Scope: "backend", Source: "b", Tier: 1},
		{Scope: "", Source: "c", Tier: 1}, // empty scope → universal.jsonl
	}
	rows, perScope, err := emitJSONL(out, examples)
	if err != nil {
		t.Fatalf("emitJSONL: %v", err)
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3", rows)
	}
	if perScope["backend"] != 2 || perScope["universal"] != 1 {
		t.Errorf("perScope = %v, want backend:2 universal:1", perScope)
	}
	if _, err := os.Stat(filepath.Join(out, "backend.jsonl")); err != nil {
		t.Errorf("backend.jsonl not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "universal.jsonl")); err != nil {
		t.Errorf("universal.jsonl not written: %v", err)
	}
}

func TestWriteManifest(t *testing.T) {
	out := t.TempDir()
	m := &Manifest{
		RunID:          "run-1",
		BuilderVersion: "v1",
		Sources:        []string{"retros", "decisions"},
		Totals:         ManifestTotals{SourcesProcessed: 2, ExamplesEmitted: 9},
	}
	if err := writeManifest(out, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, want := range []string{"run_id: run-1", "examples_emitted: 9"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("manifest missing %q:\n%s", want, raw)
		}
	}
}

func TestPackFile_SkipsBlankAndErrorsOnBadJSON(t *testing.T) {
	dir := t.TempDir()

	// Blank lines are skipped; valid records pass through.
	good := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(good, []byte(`{"a":1}`+"\n\n"+`{"b":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.jsonl")
	stats, err := Pack(PackConfig{InputDir: dir, OutPath: out})
	if err != nil {
		t.Fatalf("Pack good: %v", err)
	}
	if stats.LinesIn != 2 {
		t.Errorf("LinesIn = %d, want 2 (blank skipped)", stats.LinesIn)
	}

	// Malformed JSON line surfaces an error.
	bad := filepath.Join(dir, "sub")
	_ = os.MkdirAll(bad, 0o755)
	if err := os.WriteFile(filepath.Join(bad, "bad.jsonl"), []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(PackConfig{InputDir: bad, OutPath: filepath.Join(dir, "o2.jsonl")}); err == nil {
		t.Error("malformed json should error")
	}
}
