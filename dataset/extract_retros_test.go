package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStripSectionNumberPrefix(t *testing.T) {
	cases := map[string]string{
		"1. Execution Summary":        "Execution Summary",
		"3.1 Failure Register":        "Failure Register",
		"7.1 Actions":                 "Actions",
		"Improvement Actions":         "Improvement Actions",
		"9to5 Working Hours":          "9to5 Working Hours",
		"  10. Lessons":               "Lessons",
		"":                            "",
	}
	for in, want := range cases {
		if got := stripSectionNumberPrefix(in); got != want {
			t.Errorf("stripSectionNumberPrefix(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExtractRetros(t *testing.T) {
	tmp := t.TempDir()
	// One full-bodied retro under a real project scope, plus an
	// unknown-project dir that should be skipped, plus a too-short
	// section that should be filtered out.
	body := strings.Repeat("This is a substantive section body sentence. ", 4)
	retroBody := `# Sample Retro

## 1. Execution Summary

` + body + `

## 7. Improvement Actions

` + body + `

## 8. Stub

n/a
`
	mustWrite(t, filepath.Join(tmp, "retrospectives", "core-api", "sample-retrospective.md"), retroBody)
	mustWrite(t, filepath.Join(tmp, "retrospectives", "mystery-project", "x-retrospective.md"), retroBody)

	exs, stats, err := ExtractRetros(tmp)
	if err != nil {
		t.Fatalf("ExtractRetros: %v", err)
	}
	if got, want := stats.FilesScanned, 1; got != want {
		t.Errorf("FilesScanned=%d want %d (mystery-project should be skipped)", got, want)
	}
	if got, want := stats.ExamplesEmitted, 2; got != want {
		t.Fatalf("ExamplesEmitted=%d want %d (stub section should be dropped)", got, want)
	}
	if stats.PerScope["backend"] != 2 {
		t.Errorf("PerScope[backend]=%d want 2", stats.PerScope["backend"])
	}

	// Find both examples by section name.
	var summary, actions *Example
	for i := range exs {
		switch exs[i].Metadata["section"] {
		case "1. Execution Summary":
			summary = &exs[i]
		case "7. Improvement Actions":
			actions = &exs[i]
		}
	}
	if summary == nil || actions == nil {
		t.Fatalf("missing expected sections; got %+v", exs)
	}
	if summary.Role != "domain" {
		t.Errorf("Execution Summary should be role=domain, got %q", summary.Role)
	}
	if actions.Role != "challenge" {
		t.Errorf("Improvement Actions should be role=challenge, got %q", actions.Role)
	}
	if !strings.Contains(summary.Messages[0].Content, "Execution Summary") {
		t.Errorf("section number prefix not stripped from user prompt: %q", summary.Messages[0].Content)
	}
	if summary.Source != "retrospectives/core-api/sample-retrospective.md#1-execution-summary" {
		t.Errorf("summary Source=%q", summary.Source)
	}
	if summary.Metadata["retro"] != "sample-retrospective" {
		t.Errorf("retro metadata=%q want sample-retrospective", summary.Metadata["retro"])
	}
}
