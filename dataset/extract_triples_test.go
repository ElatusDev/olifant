package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTriples_happyPath(t *testing.T) {
	tmp := t.TempDir()
	stem := "tenant-migration"
	proj := "elatusdev-web"

	mustWrite(t, filepath.Join(tmp, "workflows", proj, stem+"-workflow.md"), `# Tenant Migration — Workflow

> meta line ignored

This is the workflow excerpt body that should be captured by digestArtifact.

## 1. Goal
later content
`)
	mustWrite(t, filepath.Join(tmp, "prompts", proj, stem+"-prompt.md"), `# Tenant Migration — Prompt

Prompt execution body excerpt.
`)
	mustWrite(t, filepath.Join(tmp, "retrospectives", proj, stem+"-retrospective.md"), `# Tenant Migration — Retrospective

Retrospective outcome body excerpt.
`)

	exs, stats, err := ExtractTriples(tmp)
	if err != nil {
		t.Fatalf("ExtractTriples: %v", err)
	}
	if got, want := stats.ExamplesEmitted, 1; got != want {
		t.Fatalf("ExamplesEmitted=%d want %d", got, want)
	}
	if stats.PerScope["webapp"] != 1 {
		t.Errorf("PerScope[webapp]=%d want 1", stats.PerScope["webapp"])
	}

	e := exs[0]
	if e.Tier != 2 || e.Role != "prompt_build" || e.Family != "lifecycle-triple" || e.Scope != "webapp" {
		t.Errorf("triple wrong shape: %+v", e)
	}
	if e.Source != "lifecycle/elatusdev-web/tenant-migration" {
		t.Errorf("Source=%q", e.Source)
	}
	body := e.Messages[1].Content
	for _, want := range []string{
		"## Workflow (design)",
		"## Prompt (execution)",
		"## Retrospective (outcome)",
		"workflow excerpt body that should be captured",
		"Prompt execution body excerpt",
		"Retrospective outcome body excerpt",
		"cite: workflows/elatusdev-web/tenant-migration-workflow.md",
		"cite: prompts/elatusdev-web/tenant-migration-prompt.md",
		"cite: retrospectives/elatusdev-web/tenant-migration-retrospective.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--body--\n%s", want, body)
		}
	}
}

func TestExtractTriples_skipsPartial(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "workflows", "core-api", "only-design-workflow.md"), `# only-design

stub
`)
	// no matching prompt/retro
	mustWrite(t, filepath.Join(tmp, "prompts", "core-api", "no-pair-prompt.md"), `# no-pair

stub
`)

	exs, stats, err := ExtractTriples(tmp)
	if err != nil {
		t.Fatalf("ExtractTriples: %v", err)
	}
	if got, want := stats.EntriesParsed, 2; got != want {
		t.Errorf("EntriesParsed=%d want %d (2 stems indexed)", got, want)
	}
	if got, want := stats.ExamplesEmitted, 0; got != want {
		t.Errorf("ExamplesEmitted=%d want 0 (no complete triples)", got)
	}
	if len(exs) != 0 {
		t.Errorf("len(exs)=%d want 0", len(exs))
	}
}

func TestExtractTriples_unknownProjectSkipped(t *testing.T) {
	tmp := t.TempDir()
	proj := "mystery-team"
	stem := "x"
	mustWrite(t, filepath.Join(tmp, "workflows", proj, stem+"-workflow.md"), "# x\nbody\n")
	mustWrite(t, filepath.Join(tmp, "prompts", proj, stem+"-prompt.md"), "# x\nbody\n")
	mustWrite(t, filepath.Join(tmp, "retrospectives", proj, stem+"-retrospective.md"), "# x\nbody\n")

	_, stats, err := ExtractTriples(tmp)
	if err != nil {
		t.Fatalf("ExtractTriples: %v", err)
	}
	if stats.ExamplesEmitted != 0 {
		t.Errorf("unknown project should not emit; stats=%+v", stats)
	}
}

func TestDigestArtifact(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "doc.md")
	mustWrite(t, p, "# Title Line\n\n> blockquote-skip\n\nFirst body paragraph.\nSecond line of same para.\n\nNext para should not be in excerpt.\n")
	d, err := digestArtifact(p)
	if err != nil {
		t.Fatalf("digestArtifact: %v", err)
	}
	if d.Title != "Title Line" {
		t.Errorf("Title=%q want Title Line", d.Title)
	}
	if d.Excerpt != "First body paragraph. Second line of same para." {
		t.Errorf("Excerpt=%q", d.Excerpt)
	}
}
