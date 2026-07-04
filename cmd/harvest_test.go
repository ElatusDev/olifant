package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func harvestFixture(t *testing.T, nSignals int) (platformRoot, reactions string) {
	t.Helper()
	platformRoot = t.TempDir()
	turns := filepath.Join(platformRoot, "knowledge-base", "short-term", "turns")
	if err := os.MkdirAll(turns, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformRoot, "knowledge-base", "README.md"), []byte("kb"), 0o644); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 0; i < nSignals; i++ {
		id := "t-" + string(rune('a'+i))
		body := "turn_id: " + id + "\nts: \"2026-07-01T00:00:00Z\"\nsubcommand: challenge\nscope:\n    - backend\nrequest: real question " + id +
			"\nchallenge:\n    verdict: VALID\n    proceed: proceed\n    output: x\n    cite_attempts: 1\nperformance:\n    elapsed_ms: 1\n"
		if err := os.WriteFile(filepath.Join(turns, id+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, `{"turn_id":"`+id+`","ts":"2026-07-01T0`+string(rune('0'+i%10))+`:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"accept"}`)
	}
	reactions = filepath.Join(platformRoot, "reactions.jsonl")
	if err := os.WriteFile(reactions, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(platformRoot)
	return platformRoot, reactions
}

func TestHarvest_GE0RefusesBelowThreshold(t *testing.T) {
	root, reactions := harvestFixture(t, 3)
	code := Harvest([]string{"-reactions", reactions, "-cursor", filepath.Join(root, "cur"), "-out", root})
	if code != 1 {
		t.Errorf("below threshold: exit %d, want 1", code)
	}
}

func TestHarvest_RunReportAndCursorDedup(t *testing.T) {
	root, reactions := harvestFixture(t, 16)
	cursor := filepath.Join(root, "harvest", "cursor")
	out := filepath.Join(root, "harvest")

	if code := Harvest([]string{"-reactions", reactions, "-cursor", cursor, "-out", out, "-threshold", "15"}); code != 0 {
		t.Fatalf("run: exit %d, want 0", code)
	}
	reports, _ := filepath.Glob(filepath.Join(out, "report-*.md"))
	if len(reports) != 1 {
		t.Fatalf("reports = %v", reports)
	}
	body, _ := os.ReadFile(reports[0])
	if !strings.Contains(string(body), "Eval-case candidates") || !strings.Contains(string(body), "PROPOSE-ONLY") {
		t.Error("report missing sections")
	}

	// Second run: cursor dedup → 0 proposals.
	if code := Harvest([]string{"-reactions", reactions, "-cursor", cursor, "-out", out, "-threshold", "15"}); code != 0 {
		t.Fatalf("rerun: exit %d", code)
	}
	reports2, _ := filepath.Glob(filepath.Join(out, "report-*.md"))
	body2, _ := os.ReadFile(reports2[0])
	if !strings.Contains(string(body2), "Eval-case candidates (runnable skeletons, accept-only) (0)") {
		t.Error("cursor dedup failed: proposals re-emitted")
	}
}

func TestHarvestAccept_AppendsCaseAndGuardsSuite(t *testing.T) {
	root, reactions := harvestFixture(t, 16)
	suite := filepath.Join(root, "real-usage-v1.yaml")

	if code := Harvest([]string{"accept", "-reactions", reactions, "-turn", "t-a", "-suite", suite}); code != 0 {
		t.Fatalf("accept: exit %d, want 0", code)
	}
	body, _ := os.ReadFile(suite)
	if !strings.Contains(string(body), "suite_id: real-usage-v1") || !strings.Contains(string(body), "id: t-a") {
		t.Errorf("suite content: %s", body)
	}

	if code := Harvest([]string{"accept", "-reactions", reactions, "-turn", "t-a", "-suite", "eval/suites/code-feeding-v2.yaml"}); code != 2 {
		t.Error("code-feeding guard failed (D-HV4)")
	}
	if code := Harvest([]string{"accept", "-reactions", reactions, "-turn", "nope", "-suite", suite}); code != 1 {
		t.Error("unknown turn should exit 1")
	}
}
