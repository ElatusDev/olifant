package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
)

func TestNewRunID(t *testing.T) {
	ts := time.Date(2026, 6, 19, 8, 5, 3, 0, time.UTC)
	if got := NewRunID(ts, "suite-x"); got != "2026-06-19T08-05-03Z-suite-x" {
		t.Errorf("NewRunID = %q", got)
	}
}

func TestPickInt(t *testing.T) {
	cases := []struct{ a, b, def, want int }{
		{5, 9, 1, 5},  // a wins
		{0, 9, 1, 9},  // b wins
		{0, 0, 1, 1},  // default
		{-1, 0, 7, 7}, // non-positive a falls through
	}
	for _, c := range cases {
		if got := pickInt(c.a, c.b, c.def); got != c.want {
			t.Errorf("pickInt(%d,%d,%d) = %d, want %d", c.a, c.b, c.def, got, c.want)
		}
	}
}

func TestPickStr(t *testing.T) {
	if got := pickStr("a", "b", "d"); got != "a" {
		t.Errorf("pickStr a-wins = %q", got)
	}
	if got := pickStr("", "b", "d"); got != "b" {
		t.Errorf("pickStr b-wins = %q", got)
	}
	if got := pickStr("", "", "d"); got != "d" {
		t.Errorf("pickStr default = %q", got)
	}
}

func TestLanguageHintForPath(t *testing.T) {
	cases := map[string]string{
		"Foo.java": "java", "a.kt": "kotlin", "a.ts": "typescript", "a.tsx": "tsx",
		"a.js": "javascript", "a.jsx": "jsx", "a.go": "go", "a.py": "python",
		"a.swift": "swift", "a.tf": "terraform", "a.sql": "sql", "a.yaml": "yaml",
		"a.json": "json", "a.xml": "xml", "a.sh": "shell", "a.unknown": "",
	}
	for path, want := range cases {
		if got := languageHintForPath(path); got != want {
			t.Errorf("languageHintForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildRequestForCase_LiteralRequest(t *testing.T) {
	got, err := buildRequestForCase(Case{ID: "c1", Request: "do X"}, "/root")
	if err != nil {
		t.Fatalf("buildRequestForCase: %v", err)
	}
	if got != "do X" {
		t.Errorf("literal request = %q", got)
	}
}

func TestBuildRequestForCase_BothSetIsError(t *testing.T) {
	_, err := buildRequestForCase(Case{ID: "c1", File: "f.go", Request: "x"}, "/root")
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Errorf("want both-set error, got %v", err)
	}
}

func TestBuildRequestForCase_NeitherSetIsError(t *testing.T) {
	_, err := buildRequestForCase(Case{ID: "c1"}, "/root")
	if err == nil || !strings.Contains(err.Error(), "neither") {
		t.Errorf("want neither-set error, got %v", err)
	}
}

func TestBuildRequestForCase_FileWrapsWithLangFence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Foo.java"), []byte("class Foo {}\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := buildRequestForCase(Case{ID: "c1", File: "Foo.java"}, root)
	if err != nil {
		t.Fatalf("buildRequestForCase: %v", err)
	}
	if !strings.Contains(got, "```java\nclass Foo {}\n```") {
		t.Errorf("fenced body missing/trailing-newline not trimmed:\n%s", got)
	}
	if !strings.Contains(got, "File: Foo.java") {
		t.Errorf("file header missing:\n%s", got)
	}
}

func TestBuildRequestForCase_MissingFileErrors(t *testing.T) {
	_, err := buildRequestForCase(Case{ID: "c1", File: "nope.go"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("want read error, got %v", err)
	}
}

func TestToFirstAttemptViolations(t *testing.T) {
	if got := toFirstAttemptViolations(nil); got != nil {
		t.Errorf("nil input = %v, want nil", got)
	}
	in := []challenge.Violation{
		{Severity: challenge.SeverityBlocker, Code: "cite_unresolved", Location: "cites[0]", Value: "D999", Note: "n"},
		{Severity: challenge.SeverityWarning, Code: "soft", Location: "x"},
	}
	out := toFirstAttemptViolations(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Severity != challenge.SeverityBlocker.String() || out[0].Value != "D999" {
		t.Errorf("blocker mapped wrong: %+v", out[0])
	}
	if out[1].Severity != challenge.SeverityWarning.String() {
		t.Errorf("warning severity = %q", out[1].Severity)
	}
}

func TestWriteReport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "report.yaml")
	r := &Report{RunID: "r1", SuiteID: "s1", TotalCases: 3, CleanCases: 2}
	if err := writeReport(p, r); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "# Olifant eval run report") {
		t.Errorf("missing header:\n%s", raw)
	}
	if !strings.Contains(string(raw), "run_id: r1") {
		t.Errorf("body missing run_id:\n%s", raw)
	}
}

func TestLoadSuite(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(good, []byte("suite_id: s1\ncases:\n  - id: c1\n    request: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSuite(good)
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	if s.SuiteID != "s1" || len(s.Cases) != 1 {
		t.Errorf("parsed suite = %+v", s)
	}
}

func TestLoadSuite_Errors(t *testing.T) {
	dir := t.TempDir()

	if _, err := LoadSuite(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file should error")
	}

	noID := filepath.Join(dir, "noid.yaml")
	_ = os.WriteFile(noID, []byte("cases:\n  - id: c1\n    request: hi\n"), 0o644)
	if _, err := LoadSuite(noID); err == nil || !strings.Contains(err.Error(), "suite_id is required") {
		t.Errorf("want suite_id error, got %v", err)
	}

	noCases := filepath.Join(dir, "nocases.yaml")
	_ = os.WriteFile(noCases, []byte("suite_id: s1\n"), 0o644)
	if _, err := LoadSuite(noCases); err == nil || !strings.Contains(err.Error(), "no cases") {
		t.Errorf("want no-cases error, got %v", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("suite_id: [unterminated"), 0o644)
	if _, err := LoadSuite(bad); err == nil {
		t.Error("invalid yaml should error")
	}
}

func TestEvalExpected(t *testing.T) {
	maxB := 0

	// All-pass: verdict matches, blockers within cap, required cite present,
	// forbidden cite absent.
	exp := &Expected{
		Verdict:          "proceed",
		MaxBlockers:      &maxB,
		MustCiteAnyOf:    []string{"D154"},
		MustNotCiteAnyOf: []string{"D999"},
	}
	result := &CaseResult{Verdict: "proceed", Blockers: 0}
	em := evalExpected(exp, result, `{"verdict":"proceed","cites":["D154"]}`)
	if !em.VerdictPassed || !em.BlockersPassed || !em.MustCitePassed || !em.MustNotCitePassed {
		t.Errorf("all-pass expectation failed: %+v", em)
	}

	// Verdict mismatch + blocker over cap + forbidden cite present.
	result2 := &CaseResult{Verdict: "abort", Blockers: 2}
	em2 := evalExpected(exp, result2, `{"cites":["D999"]}`)
	if em2.VerdictPassed {
		t.Error("verdict should fail on mismatch")
	}
	if em2.BlockersPassed {
		t.Error("blockers should fail over cap")
	}
	if em2.MustCitePassed {
		t.Error("must-cite should fail when D154 absent")
	}
	if em2.MustNotCitePassed {
		t.Error("must-not-cite should fail when D999 present")
	}
}

func TestEvalExpected_EmptyContractDefaultsPass(t *testing.T) {
	// No verdict, no blocker cap, no cite constraints → everything passes,
	// cite flags left at zero value.
	em := evalExpected(&Expected{}, &CaseResult{Blockers: 5}, "")
	if !em.VerdictPassed || !em.BlockersPassed {
		t.Errorf("empty contract should pass verdict/blockers: %+v", em)
	}
}
