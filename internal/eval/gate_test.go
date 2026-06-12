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
