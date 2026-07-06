package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rpt(clean, blockers int, cases ...CaseResult) *Report {
	return &Report{TotalCases: len(cases), CleanCases: clean, TotalBlockers: blockers, Cases: cases}
}

func TestGateThresholds(t *testing.T) {
	cfg := GateConfig{MinClean: 11, MaxBlockers: 0}
	cases := []struct {
		name    string
		report  *Report
		base    *Report
		pass    bool
		wantSub string
	}{
		{"pass at 12/12 0B", rpt(12, 0,
			CaseResult{CaseID: "c1"}, CaseResult{CaseID: "c2"}), nil, true, ""},
		{"pass at 11 clean", rpt(11, 0, CaseResult{CaseID: "c1"}), nil, true, ""},
		{"fail below min clean", rpt(10, 0, CaseResult{CaseID: "c1"}), nil, false, "clean cases"},
		{"fail on blockers", rpt(12, 1, CaseResult{CaseID: "c1", Blockers: 1}), nil, false, "BLOCKERs 1 above"},
		{"fail on case error", rpt(11, 0, CaseResult{CaseID: "c1", Error: "boom"}), nil, false, "ERRORed"},
		{"fail on new blocker vs baseline", rpt(11, 1,
			CaseResult{CaseID: "c1", Blockers: 1}),
			rpt(12, 0, CaseResult{CaseID: "c1"}), false, "new BLOCKERs on previously-clean cases: c1"},
		{"baseline-blocked case stays non-new", rpt(11, 1,
			CaseResult{CaseID: "c1", Blockers: 1}),
			rpt(11, 1, CaseResult{CaseID: "c1", Blockers: 1}), false, "BLOCKERs 1 above"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// thresholds intentionally permissive on count fields the fixture
			// doesn't model; MinClean is checked against CleanCases directly
			v := Gate(tc.report, tc.base, cfg)
			if v.Pass != tc.pass {
				t.Fatalf("Pass = %v, want %v (reasons: %v)", v.Pass, tc.pass, v.Reasons)
			}
			if tc.wantSub != "" && !strings.Contains(strings.Join(v.Reasons, "; "), tc.wantSub) {
				t.Fatalf("reasons %v missing %q", v.Reasons, tc.wantSub)
			}
		})
	}
}

func TestGateNewBlockerNotFlaggedWhenBaselineHadWarning(t *testing.T) {
	// A baseline case with W>0 is not "previously clean" — blocking it now
	// is a count failure but not a new-blocker regression.
	report := rpt(11, 1, CaseResult{CaseID: "c1", Blockers: 1})
	base := rpt(11, 0, CaseResult{CaseID: "c1", Warnings: 1})
	v := Gate(report, base, GateConfig{MinClean: 0, MaxBlockers: 5})
	if len(v.NewBlockers) != 0 {
		t.Fatalf("NewBlockers = %v, want none", v.NewBlockers)
	}
}

func TestDiffTable(t *testing.T) {
	report := rpt(0, 1,
		CaseResult{CaseID: "c1", Blockers: 1, Warnings: 0},
		CaseResult{CaseID: "c2", Error: "dead"})
	base := rpt(0, 0, CaseResult{CaseID: "c1"})
	out := DiffTable(report, base)
	for _, want := range []string{"c1", "1/0", "0/0", "ERROR", "—"} {
		if !strings.Contains(out, want) {
			t.Fatalf("DiffTable missing %q:\n%s", want, out)
		}
	}
}

func TestLoadReport(t *testing.T) {
	dir := t.TempDir()
	body := "run_id: r1\nsuite_id: s\ntotal_cases: 1\nclean_cases: 1\ncases:\n  - case_id: c1\n"
	if err := os.WriteFile(filepath.Join(dir, "report.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadReport(dir)
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	if r.RunID != "r1" || r.CleanCases != 1 || len(r.Cases) != 1 {
		t.Fatalf("bad parse: %+v", r)
	}
	if _, err := LoadReport(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestReceiptRoundTripAndFilters(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log", "receipts.log")
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := Receipt{Verdict: "PASS", GitSHA: "aaa", SuiteSHA: "s1", CorpusSHA: "c1", RunID: "r1", Timestamp: "2026-06-12T01:00:00Z"}
	newer := Receipt{Verdict: "FAIL", GitSHA: "bbb", SuiteSHA: "s1", CorpusSHA: "c1", RunID: "r2", Timestamp: "2026-06-12T02:00:00Z"}
	for _, r := range []Receipt{older, newer} {
		if err := WriteReceipt(runDir, logPath, r); err != nil {
			t.Fatalf("WriteReceipt: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, "gate-pass.yaml")); err != nil {
		t.Fatalf("gate-pass.yaml not written: %v", err)
	}

	got, err := LatestReceipt(logPath, Receipt{})
	if err != nil || got == nil || got.RunID != "r2" {
		t.Fatalf("unfiltered latest = %+v, %v; want r2", got, err)
	}
	got, err = LatestReceipt(logPath, Receipt{Verdict: "PASS"})
	if err != nil || got == nil || got.RunID != "r1" {
		t.Fatalf("PASS-filtered = %+v, %v; want r1", got, err)
	}
	got, err = LatestReceipt(logPath, Receipt{GitSHA: "zzz"})
	if err != nil || got != nil {
		t.Fatalf("no-match should be nil, got %+v, %v", got, err)
	}
	got, err = LatestReceipt(logPath, Receipt{SuiteSHA: "s1", CorpusSHA: "c1"})
	if err != nil || got == nil || got.RunID != "r2" {
		t.Fatalf("sha-filtered = %+v, %v; want r2", got, err)
	}
}

func TestLatestReceiptSuiteScoped(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "receipts.log")

	legacy := Receipt{Verdict: "PASS", GitSHA: "aaa", SuiteSHA: "s0", CorpusSHA: "c1", RunID: "legacy", Timestamp: "2026-06-12T01:00:00Z"}
	feeding := Receipt{Verdict: "PASS", SuiteID: "code-feeding-v2", GitSHA: "bbb", SuiteSHA: "s1", CorpusSHA: "c1", RunID: "cf", Timestamp: "2026-07-06T01:00:00Z"}
	realUsage := Receipt{Verdict: "PASS", SuiteID: "real-usage-v1", GitSHA: "bbb", SuiteSHA: "s2", CorpusSHA: "c1", RunID: "ru", Timestamp: "2026-07-06T02:00:00Z"}
	for _, r := range []Receipt{legacy, feeding, realUsage} {
		if err := WriteReceipt("", logPath, r); err != nil {
			t.Fatalf("WriteReceipt: %v", err)
		}
	}

	// Baseline lookup is suite-scoped: real-usage's newest PASS never
	// resolves to another suite's receipt (D-RG2 / AC6).
	got, err := LatestReceipt(logPath, Receipt{Verdict: "PASS", SuiteID: "code-feeding-v2"})
	if err != nil || got == nil || got.RunID != "cf" {
		t.Fatalf("code-feeding lookup = %+v, %v; want cf", got, err)
	}
	got, err = LatestReceipt(logPath, Receipt{Verdict: "PASS", SuiteID: "real-usage-v1"})
	if err != nil || got == nil || got.RunID != "ru" {
		t.Fatalf("real-usage lookup = %+v, %v; want ru", got, err)
	}

	// Pre-HV-F1 lines (no suite_id) never satisfy a SuiteID filter (AC6),
	// even when every other field matches.
	got, err = LatestReceipt(logPath, Receipt{Verdict: "PASS", SuiteID: "code-feeding-v2", GitSHA: "aaa"})
	if err != nil || got != nil {
		t.Fatalf("legacy line matched a SuiteID filter: %+v, %v", got, err)
	}

	// A SuiteID-less filter still sees every line (back-compat).
	got, err = LatestReceipt(logPath, Receipt{Verdict: "PASS"})
	if err != nil || got == nil || got.RunID != "ru" {
		t.Fatalf("unscoped lookup = %+v, %v; want ru (newest)", got, err)
	}

	// Round trip: suite_id survives the JSON log.
	if got.SuiteID != "real-usage-v1" {
		t.Fatalf("suite_id round trip = %q; want real-usage-v1", got.SuiteID)
	}
}

func TestLatestReceiptMissingLogAndCorruptLine(t *testing.T) {
	got, err := LatestReceipt(filepath.Join(t.TempDir(), "nope.log"), Receipt{})
	if err != nil || got != nil {
		t.Fatalf("missing log should be (nil, nil), got %+v, %v", got, err)
	}

	logPath := filepath.Join(t.TempDir(), "receipts.log")
	content := `{"verdict":"PASS","git_sha":"aaa","run_id":"good"}` + "\nnot-json\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = LatestReceipt(logPath, Receipt{})
	if err != nil || got == nil || got.RunID != "good" {
		t.Fatalf("corrupt-tolerant scan = %+v, %v; want good", got, err)
	}
}

func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.yaml")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := FileSHA256(p)
	if err != nil || len(h1) != 64 {
		t.Fatalf("sha = %q, %v", h1, err)
	}
	h2, err := FileSHA256(p + ".missing")
	if err != nil || h2 != "" {
		t.Fatalf("missing file should hash to empty, got %q, %v", h2, err)
	}
}

func TestFirstAttemptBlockerReport(t *testing.T) {
	// A case with first-try clean (no violations) plus a case with a
	// retry-masked BLOCKER (final B=0, attempts=2, attempt-1 had a blocker).
	report := &Report{Cases: []CaseResult{
		{CaseID: "c1", Attempts: 1, Blockers: 0}, // clean first try — silent in report
		{CaseID: "c2", Attempts: 2, Blockers: 0, FirstAttemptViolations: []FirstAttemptViolation{
			{Severity: "BLOCKER", Code: "cite_unresolved", Location: "confirms[1].cites[0]",
				Value: "eval/failure-modes/v1.yaml#FM1", Note: "value does not exist"},
			{Severity: "WARNING", Code: "weak_cite", Location: "confirms[0].cites[1]"},
		}},
	}}
	out := FirstAttemptBlockerReport(report)
	for _, want := range []string{
		"c2 (attempts=2):",
		"[cite_unresolved] value does not exist @ confirms[1].cites[0]",
		`"eval/failure-modes/v1.yaml#FM1"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c1") {
		t.Fatalf("clean case c1 leaked into report:\n%s", out)
	}
	if strings.Contains(out, "weak_cite") {
		t.Fatalf("non-BLOCKER WARNING surfaced; report should be BLOCKER-only:\n%s", out)
	}
}

func TestFirstAttemptBlockerReportEmpty(t *testing.T) {
	// Healthy report: all cases first-try clean → empty output (steady state).
	report := &Report{Cases: []CaseResult{
		{CaseID: "c1", Attempts: 1},
		{CaseID: "c2", Attempts: 1},
	}}
	if got := FirstAttemptBlockerReport(report); got != "" {
		t.Fatalf("expected empty report, got %q", got)
	}
}

func TestGateFirstTryFloor(t *testing.T) {
	healthy := rpt(12, 0, CaseResult{CaseID: "c1"})
	healthy.FirstTryPassRate = 0.92
	regressed := rpt(12, 0, CaseResult{CaseID: "c1"})
	regressed.FirstTryPassRate = 0.50

	cfg := GateConfig{MinClean: 11, MaxBlockers: 0, MinFirstTry: 0.70}
	if v := Gate(healthy, nil, cfg); !v.Pass {
		t.Fatalf("healthy run failed first-try floor: %v", v.Reasons)
	}
	if v := Gate(regressed, nil, cfg); v.Pass || !strings.Contains(strings.Join(v.Reasons, " "), "first-try") {
		t.Fatalf("retry-masked regression not caught: pass=%v reasons=%v", v.Pass, v.Reasons)
	}
	// 0 disables the floor (e.g. judging legacy qwen-era reports).
	if v := Gate(regressed, nil, GateConfig{MinClean: 11}); !v.Pass {
		t.Fatalf("disabled floor should pass: %v", v.Reasons)
	}
}
