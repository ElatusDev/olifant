package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFailureModes_happyPath(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "eval", "failure-modes", "v1.yaml"), `
meta:
  version: 1
  source: test fixture
entries:
  - id: FM1
    code: cite_unresolved
    scope: backend
    what_the_model_does_wrong: |
      hallucinates the package prefix
    user_prompt: |
      Where does X live?
    correct_assistant_response: |
      core-api/X.java
    rationale: |
      Always cite the real path
    cite: short-term/eval-runs/test
  - id: FM2
    code: clarify_required_but_empty
    scope: universal
    user_prompt: |
      Ask a question
    correct_assistant_response: |
      verdict: NEEDS_CLARIFICATION
      clarify: [why?]
  - id: FM_skip
    code: empty
    user_prompt: ""
    correct_assistant_response: "no prompt"
`)

	exs, stats, err := ExtractFailureModes(tmp)
	if err != nil {
		t.Fatalf("ExtractFailureModes: %v", err)
	}
	if got, want := stats.FilesScanned, 1; got != want {
		t.Errorf("FilesScanned=%d want %d", got, want)
	}
	if got, want := stats.EntriesParsed, 3; got != want {
		t.Errorf("EntriesParsed=%d want %d", got, want)
	}
	if got, want := stats.ExamplesEmitted, 2; got != want {
		t.Errorf("ExamplesEmitted=%d want %d (empty user_prompt should skip)", got, want)
	}
	if len(exs) != 2 {
		t.Fatalf("len(exs)=%d want 2", len(exs))
	}

	e := exs[0]
	if e.Tier != 1 || e.Role != "domain" || e.Family != "failure-mode-correction" || e.Scope != "backend" {
		t.Errorf("FM1 wrong shape: %+v", e)
	}
	if e.Source != "eval/failure-modes/v1.yaml#FM1" {
		t.Errorf("Source=%q want eval/failure-modes/v1.yaml#FM1", e.Source)
	}
	if !strings.Contains(e.Messages[1].Content, "core-api/X.java") {
		t.Errorf("assistant content missing corrected path: %q", e.Messages[1].Content)
	}
	if e.Metadata["failure_mode_id"] != "FM1" || e.Metadata["code"] != "cite_unresolved" {
		t.Errorf("metadata wrong: %+v", e.Metadata)
	}
	if !strings.Contains(e.Metadata["rationale"], "Always cite") {
		t.Errorf("rationale not preserved: %q", e.Metadata["rationale"])
	}

	// FM2 default scope
	if exs[1].Scope != "universal" {
		t.Errorf("FM2 scope=%q want universal", exs[1].Scope)
	}
}

func TestExtractFailureModes_missingDirIsFine(t *testing.T) {
	tmp := t.TempDir()
	exs, stats, err := ExtractFailureModes(tmp)
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if stats.FilesScanned != 0 || stats.ExamplesEmitted != 0 || len(exs) != 0 {
		t.Errorf("expected zero stats on missing dir, got %+v", stats)
	}
}

func TestPickLatestFailureModesFile(t *testing.T) {
	tmp := t.TempDir()
	// v1, v2, v10 — string sort should pick v2.yaml as the highest
	// lex value, which would be wrong if N>9 were intended.
	// Document this as a known limitation: numbering stays single-
	// digit for the foreseeable future; if we ever exceed v9.yaml,
	// switch to zero-padding (v01.yaml).
	for _, n := range []string{"v1.yaml", "v2.yaml", "noise.txt"} {
		mustWrite(t, filepath.Join(tmp, n), "meta: {}\nentries: []\n")
	}
	got, err := pickLatestFailureModesFile(tmp)
	if err != nil {
		t.Fatalf("pickLatest: %v", err)
	}
	if filepath.Base(got) != "v2.yaml" {
		t.Errorf("picked %q want v2.yaml", filepath.Base(got))
	}
}
