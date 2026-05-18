package dataset

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexFailureModes_dryRun(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "eval", "failure-modes", "v1.yaml"), `
meta: {version: 1, source: test}
entries:
  - id: FM1
    code: cite_unresolved
    scope: backend
    user_prompt: Where is X?
    correct_assistant_response: core-api/X.java
    rationale: Use com.akademiaplus, not com.akademia.
  - id: FM2
    code: clarify_required_but_empty
    scope: universal
    user_prompt: Ask a question.
    correct_assistant_response: |
      verdict: NEEDS_CLARIFICATION
      clarify: [why?]
    rationale: Always populate clarify[].
  - id: FM3
    code: cite_unresolved
    scope: backend
    user_prompt: ""
    correct_assistant_response: ""
    rationale: empty — should be skipped
`)

	stats, err := IndexFailureModes(context.Background(), IndexConfig{
		KBRoot: tmp,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("IndexFailureModes: %v", err)
	}
	if stats.EntriesRead != 3 {
		t.Errorf("EntriesRead=%d want 3", stats.EntriesRead)
	}
	if stats.Chunks != 2 {
		t.Errorf("Chunks=%d want 2 (empty-body should skip)", stats.Chunks)
	}
	if stats.Upserted != 0 {
		t.Errorf("Upserted=%d want 0 (dry-run)", stats.Upserted)
	}
}

func TestIndexFailureModes_missingDirReturnsZero(t *testing.T) {
	tmp := t.TempDir()
	stats, err := IndexFailureModes(context.Background(), IndexConfig{
		KBRoot: tmp,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if stats.EntriesRead != 0 || stats.Chunks != 0 {
		t.Errorf("expected zero stats on missing dir, got %+v", stats)
	}
}

func TestComposeFailureModeBody(t *testing.T) {
	e := failureModeEntry{
		ID:                       "FM1",
		UserPrompt:               "Where is X?",
		CorrectAssistantResponse: "core-api/X.java",
		Rationale:                "Use com.akademiaplus, not com.akademia.",
	}
	body := composeFailureModeBody(e)
	for _, want := range []string{
		"Q: Where is X?",
		"A: core-api/X.java",
		"Rationale: Use com.akademiaplus",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--body--\n%s", want, body)
		}
	}
}

func TestGroupFailureModesByScope(t *testing.T) {
	entries := []failureModeEntry{
		{ID: "FM1", Scope: "backend", UserPrompt: "q", CorrectAssistantResponse: "a"},
		{ID: "FM2", Scope: "", UserPrompt: "q", CorrectAssistantResponse: "a"}, // defaults to universal
		{ID: "FM3", Scope: "backend", UserPrompt: "", CorrectAssistantResponse: ""},  // body empty → drop
	}
	got := groupFailureModesByScope(entries, "eval/failure-modes/v1.yaml")
	if len(got["backend"]) != 1 {
		t.Errorf("backend chunks=%d want 1", len(got["backend"]))
	}
	if len(got["universal"]) != 1 {
		t.Errorf("universal chunks=%d want 1", len(got["universal"]))
	}
	if got["backend"][0].Metadata["source"] != "eval/failure-modes/v1.yaml#FM1" {
		t.Errorf("source meta=%q", got["backend"][0].Metadata["source"])
	}
	if got["backend"][0].ID == got["universal"][0].ID {
		t.Errorf("chunk IDs should differ: %q == %q", got["backend"][0].ID, got["universal"][0].ID)
	}
}
