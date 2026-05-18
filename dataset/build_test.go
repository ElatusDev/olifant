package dataset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild_endToEnd(t *testing.T) {
	tmp := t.TempDir()

	// Minimal KB with one entry per Tier-1 source.
	mustWrite(t, filepath.Join(tmp, "decisions", "log.yaml"), `
decisions:
  - id: D1
    name: Sample Decision
    decision: do X
    rationale: because Y
`)
	mustWrite(t, filepath.Join(tmp, "anti-patterns", "catalog.yaml"), `
entries:
  - id: AP1
    name: Sample Antipattern
    context: in this context
    root_cause: rushed work
`)
	mustWrite(t, filepath.Join(tmp, "patterns", "backend.md"), `# Backend

## Sample Pattern

`+strings.Repeat("Body text. ", 10)+`
`)
	mustWrite(t, filepath.Join(tmp, "retrospectives", "core-api", "sample-retrospective.md"),
		"# Sample\n\n## 1. Execution Summary\n\n"+strings.Repeat("Body. ", 20)+"\n")
	mustWrite(t, filepath.Join(tmp, "workflows", "core-api", "sample-workflow.md"),
		"# Sample WF\n\nworkflow body\n")
	mustWrite(t, filepath.Join(tmp, "prompts", "core-api", "sample-prompt.md"),
		"# Sample Prompt\n\nprompt body\n")
	// retrospective for the triple — same stem
	mustWrite(t, filepath.Join(tmp, "retrospectives", "core-api", "sample-cycle-retrospective.md"),
		"# Cycle Retro\n\nretro body\n")
	mustWrite(t, filepath.Join(tmp, "workflows", "core-api", "sample-cycle-workflow.md"),
		"# Cycle WF\n\ncycle wf body\n")
	mustWrite(t, filepath.Join(tmp, "prompts", "core-api", "sample-cycle-prompt.md"),
		"# Cycle Prompt\n\ncycle prompt body\n")

	out := filepath.Join(tmp, "training", "2026-05-18")
	stats, err := Build(BuildConfig{
		KBRoot:     tmp,
		OutDir:     out,
		Sources:    nil, // all
		WriteJSONL: true,
		Verbose:    false,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.SourcesProcessed != 5 {
		t.Errorf("SourcesProcessed=%d want 5", stats.SourcesProcessed)
	}
	if stats.ExamplesEmitted < 5 {
		t.Errorf("ExamplesEmitted=%d want >=5", stats.ExamplesEmitted)
	}

	// Verify each per-source subdir got a JSONL.
	for _, src := range AllSources {
		// Triples only fire when all 3 artifacts present; tested above.
		dir := filepath.Join(out, src.SubDir())
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("source %s subdir missing: %v", src, err)
			continue
		}
		var jsonls int
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				jsonls++
			}
		}
		if jsonls == 0 {
			t.Errorf("source %s emitted no jsonl files in %s", src, dir)
		}
	}

	// Verify manifest exists and parses.
	manifestPath := filepath.Join(out, "manifest.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.yaml missing: %v", err)
	}

	// Verify one of the jsonl rows round-trips as Example.
	dec := filepath.Join(out, "tier1-decisions", "universal.jsonl")
	raw, err := os.ReadFile(dec)
	if err != nil {
		t.Fatalf("read %s: %v", dec, err)
	}
	first := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0]
	var ex Example
	if err := json.Unmarshal([]byte(first), &ex); err != nil {
		t.Fatalf("decode first jsonl row: %v\nrow: %s", err, first)
	}
	if ex.Tier != 1 || ex.Role != "domain" || ex.Family != "decision-qa" {
		t.Errorf("decoded example wrong shape: %+v", ex)
	}
}

func TestDedupeAndOrder(t *testing.T) {
	in := []SourceKind{SourceTriples, SourceRetros, SourceRetros, SourceDecisions}
	out := dedupeAndOrder(in)
	want := []SourceKind{SourceRetros, SourceDecisions, SourceTriples}
	if len(out) != len(want) {
		t.Fatalf("len=%d want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("[%d]=%q want %q", i, out[i], want[i])
		}
	}
}
