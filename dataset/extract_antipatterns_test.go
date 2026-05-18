package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractAntipatterns(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "anti-patterns", "catalog.yaml"), `
entries:
  - id: AP1
    name: Raw SQL for PII Writes
    context: Bulk-loading data into MariaDB that includes PII fields
    what_happened: |-
      Bypassing JPA skips encryption converters
    root_cause: Performance instinct
    alternative: |-
      Always use JPA entities for PII writes
    source: etl-service workflow §9 Hard Rules
  - id: AP2
    name: Single Transaction for Large Batch
    context: Loading thousands of rows in one operation
    root_cause: Simpler code
    source: etl-service workflow §9.5
  - id: AP_skip
    name: ""
    context: should be skipped
`)
	exs, stats, err := ExtractAntipatterns(tmp)
	if err != nil {
		t.Fatalf("ExtractAntipatterns: %v", err)
	}
	if got, want := stats.EntriesParsed, 3; got != want {
		t.Errorf("EntriesParsed=%d want %d", got, want)
	}
	if got, want := stats.ExamplesEmitted, 2; got != want {
		t.Errorf("ExamplesEmitted=%d want %d (empty-name should skip)", got, want)
	}
	if len(exs) != 2 {
		t.Fatalf("len(exs)=%d want 2", len(exs))
	}

	e := exs[0]
	if e.Tier != 1 || e.Role != "challenge" || e.Family != "antipattern-challenge" || e.Scope != "universal" {
		t.Errorf("first ex wrong shape: %+v", e)
	}
	if !strings.Contains(e.Messages[0].Content, "Would it be ok") {
		t.Errorf("user prompt missing challenge framing: %q", e.Messages[0].Content)
	}
	if !strings.Contains(e.Messages[0].Content, "Raw SQL for PII Writes") {
		t.Errorf("user prompt missing AP name: %q", e.Messages[0].Content)
	}
	if !strings.HasPrefix(e.Messages[1].Content, "verdict: INVALID (AP1)") {
		t.Errorf("assistant should open with verdict: INVALID (AP1), got: %q", e.Messages[1].Content)
	}
	if !strings.Contains(e.Messages[1].Content, "Use instead: ") {
		t.Errorf("assistant missing alternative section: %q", e.Messages[1].Content)
	}
	if e.Source != "anti-patterns/catalog.yaml#AP1" {
		t.Errorf("Source=%q want anti-patterns/catalog.yaml#AP1", e.Source)
	}

	// AP2 has no what_happened or alternative — should still emit
	// (carries root_cause + source).
	e2 := exs[1]
	if !strings.Contains(e2.Messages[1].Content, "Root cause: Simpler code") {
		t.Errorf("AP2 missing root cause: %q", e2.Messages[1].Content)
	}
	if strings.Contains(e2.Messages[1].Content, "Use instead:") {
		t.Errorf("AP2 should not include Use instead (no alternative): %q", e2.Messages[1].Content)
	}
}
