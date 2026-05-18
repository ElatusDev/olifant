package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractDecisions_minimal(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "decisions", "log.yaml"), `
decisions:
  - id: D1
    name: MongoDB as ETL Staging DB
    date: "2026-03-09"
    context: |-
      Client data migration needs a staging area
    decision: |-
      Use MongoDB for staging, MariaDB for production
    alternatives: |-
      MariaDB temp tables (too rigid)
    rationale: |-
      Raw Excel data is semi-structured
    outcome: |-
      Pending
    source: etl-service-workflow.md §2
  - id: D2
    name: ETL as Core-API Module
    decision: |-
      Place ETL inside core-api as a module
    rationale: |-
      Avoid a second service to deploy
`)

	exs, stats, err := ExtractDecisions(tmp)
	if err != nil {
		t.Fatalf("ExtractDecisions: %v", err)
	}
	if got, want := stats.FilesScanned, 1; got != want {
		t.Errorf("FilesScanned=%d want %d", got, want)
	}
	if got, want := stats.EntriesParsed, 2; got != want {
		t.Errorf("EntriesParsed=%d want %d", got, want)
	}
	if got, want := stats.ExamplesEmitted, 2; got != want {
		t.Errorf("ExamplesEmitted=%d want %d", got, want)
	}
	if got, want := stats.PerScope["universal"], 2; got != want {
		t.Errorf("PerScope[universal]=%d want %d", got, want)
	}
	if got, want := len(exs), 2; got != want {
		t.Fatalf("len(examples)=%d want %d", got, want)
	}

	e := exs[0]
	if e.Tier != 1 || e.Role != "domain" || e.Family != "decision-qa" || e.Scope != "universal" {
		t.Errorf("first example wrong shape: %+v", e)
	}
	if e.Source != "decisions/log.yaml#D1" {
		t.Errorf("Source=%q want decisions/log.yaml#D1", e.Source)
	}
	if !strings.Contains(e.Messages[1].Content, "Use MongoDB for staging") {
		t.Errorf("assistant content missing decision body: %q", e.Messages[1].Content)
	}
	if !strings.Contains(e.Messages[1].Content, "Alternatives considered:") {
		t.Errorf("assistant content missing alternatives section: %q", e.Messages[1].Content)
	}
	if !strings.Contains(e.Messages[0].Content, "MongoDB as ETL Staging DB") {
		t.Errorf("user question missing decision name: %q", e.Messages[0].Content)
	}
	if e.Metadata["decision_id"] != "D1" {
		t.Errorf("metadata.decision_id=%q want D1", e.Metadata["decision_id"])
	}
}

func TestExtractDecisions_skipsEmpty(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "decisions", "log.yaml"), `
decisions:
  - id: D1
    name: Has-Body
    decision: actually do X
  - id: D2
    name: No-Body-No-Rationale
    context: just a question
`)
	exs, stats, err := ExtractDecisions(tmp)
	if err != nil {
		t.Fatalf("ExtractDecisions: %v", err)
	}
	if got, want := stats.EntriesParsed, 2; got != want {
		t.Errorf("EntriesParsed=%d want %d", got, want)
	}
	if got, want := stats.ExamplesEmitted, 1; got != want {
		t.Errorf("ExamplesEmitted=%d want %d (D2 should be skipped)", got, want)
	}
	if len(exs) != 1 || exs[0].Metadata["decision_id"] != "D1" {
		t.Errorf("wrong example survived: %+v", exs)
	}
}

func TestExtractDecisions_duplicateIDs(t *testing.T) {
	// log.yaml meta.notes says D20 appears twice; verify we
	// disambiguate the Source field rather than collide.
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "decisions", "log.yaml"), `
decisions:
  - id: D20
    name: First D20
    decision: do A
  - id: D20
    name: Second D20
    decision: do B
`)
	exs, _, err := ExtractDecisions(tmp)
	if err != nil {
		t.Fatalf("ExtractDecisions: %v", err)
	}
	if len(exs) != 2 {
		t.Fatalf("len=%d want 2", len(exs))
	}
	if exs[0].Source != "decisions/log.yaml#D20" {
		t.Errorf("first Source=%q want decisions/log.yaml#D20", exs[0].Source)
	}
	if exs[1].Source != "decisions/log.yaml#D20-2" {
		t.Errorf("second Source=%q want decisions/log.yaml#D20-2", exs[1].Source)
	}
}

// mustWrite writes content to path, creating parent dirs. Shared
// helper for all extractor tests in this package.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
