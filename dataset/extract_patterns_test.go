package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Domain Object Pattern":          "domain-object-pattern",
		"  Trim & punct! ":               "trim-punct",
		"Already-Hyphenated_Title":       "already-hyphenated-title",
		"":                               "",
		"###":                            "",
		"A   B":                          "a-b",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExtractPatterns(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "patterns", "backend.md"), `# Backend Patterns

intro paragraph that should be dropped (no H2 yet).

## Domain Object Pattern

Body of the first pattern.

` + "```java" + `
## not-a-header (inside fence)
` + "```" + `

Still part of the Domain Object Pattern body.

## Parallel Factory Pipeline

Body of the second pattern.
`)
	mustWrite(t, filepath.Join(tmp, "patterns", "frontend.md"), `# Frontend

## Feature Isolation

Body.
`)
	// Unknown basename — should be skipped to keep scope deterministic.
	mustWrite(t, filepath.Join(tmp, "patterns", "wildcard.md"), `# X
## A pattern
body
`)

	exs, stats, err := ExtractPatterns(tmp)
	if err != nil {
		t.Fatalf("ExtractPatterns: %v", err)
	}
	if got, want := stats.FilesScanned, 2; got != want {
		t.Errorf("FilesScanned=%d want %d (wildcard.md should be skipped)", got, want)
	}
	if got, want := stats.ExamplesEmitted, 3; got != want {
		t.Fatalf("ExamplesEmitted=%d want %d", got, want)
	}
	if stats.PerScope["backend"] != 2 || stats.PerScope["webapp"] != 1 {
		t.Errorf("PerScope wrong: %+v", stats.PerScope)
	}

	var dom *Example
	for i := range exs {
		if exs[i].Metadata["pattern_name"] == "Domain Object Pattern" {
			dom = &exs[i]
			break
		}
	}
	if dom == nil {
		t.Fatal("missing Domain Object Pattern example")
	}
	if dom.Scope != "backend" || dom.Role != "domain" || dom.Family != "pattern-section" {
		t.Errorf("DOP wrong shape: %+v", *dom)
	}
	if dom.Source != "patterns/backend.md#domain-object-pattern" {
		t.Errorf("DOP Source=%q want patterns/backend.md#domain-object-pattern", dom.Source)
	}
	// Fence-respect check: the fenced "## not-a-header" must NOT have
	// split the section.
	if !strings.Contains(dom.Messages[1].Content, "Still part of the Domain Object Pattern body") {
		t.Errorf("fenced ## misinterpreted as header — DOP body truncated:\n%s", dom.Messages[1].Content)
	}
	if !strings.Contains(dom.Messages[0].Content, "Domain Object Pattern") {
		t.Errorf("user prompt missing pattern name: %q", dom.Messages[0].Content)
	}
}
