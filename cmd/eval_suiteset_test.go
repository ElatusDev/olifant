package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/eval"
)

// writeSuiteN writes a suite with n clean-shaped cases. Real-usage cases are
// harvest-shaped: id/scope/request only, no default: block (IA1).
func writeSuiteN(t *testing.T, path, suiteID string, n int) {
	t.Helper()
	body := "suite_id: " + suiteID + "\ncases:\n"
	for i := 1; i <= n; i++ {
		body += fmt.Sprintf("  - id: c%02d\n    scope: [backend]\n    request: add a tenant scoped invoice entity\n", i)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readReceipts(t *testing.T) []eval.Receipt {
	t.Helper()
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".olifant", "eval-gate", "receipts.log"))
	if err != nil {
		t.Fatalf("read receipts: %v", err)
	}
	var out []eval.Receipt
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r eval.Receipt
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("corrupt receipt line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// TestEvalGate_SuiteSet_PassMintsPerSuiteReceipts covers AC1/AC2/AC3: both
// suites run, each judged by its own thresholds — real-usage min-clean is
// DERIVED (2 cases here; a hard-coded 4 would fail) — and each mints a
// suite-scoped receipt. Also asserts the gate never rewrites the
// harvest-owned suite file (D-RG4).
func TestEvalGate_SuiteSet_PassMintsPerSuiteReceipts(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	feeding := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	realUsage := filepath.Join(kb, "eval", "suites", "real-usage-v1.yaml")
	writeSuiteN(t, feeding, "code-feeding-v2", 12) // set-mode threshold: clean ≥ 11
	writeSuiteN(t, realUsage, "real-usage-v1", 2)  // derived: clean ≥ 2
	before, _ := os.ReadFile(realUsage)

	if code := evalGate(nil); code != gateExitPass {
		t.Fatalf("eval gate (suite set) = %d, want %d (PASS)", code, gateExitPass)
	}

	after, _ := os.ReadFile(realUsage)
	if string(before) != string(after) {
		t.Fatal("gate rewrote the harvest-owned suite file (D-RG4)")
	}

	var ids []string
	for _, r := range readReceipts(t) {
		if r.Verdict != "PASS" {
			t.Errorf("receipt %s verdict = %s, want PASS", r.SuiteID, r.Verdict)
		}
		ids = append(ids, r.SuiteID)
	}
	want := []string{"code-feeding-v2", "real-usage-v1"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("receipt suite ids = %v, want %v", ids, want)
	}
}

// TestEvalGate_SuiteSet_MissingRealUsageIsNamedSkip covers AC5 (D-RG5): the
// optional suite file being absent degrades to a named SKIPPED receipt while
// the required baseline still decides the verdict — never silent, never fatal.
func TestEvalGate_SuiteSet_MissingRealUsageIsNamedSkip(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	writeSuiteN(t, filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml"), "code-feeding-v2", 12)

	if code := evalGate(nil); code != gateExitPass {
		t.Fatalf("eval gate (real-usage missing) = %d, want %d", code, gateExitPass)
	}

	receipts := readReceipts(t)
	var skip *eval.Receipt
	for i := range receipts {
		if receipts[i].Verdict == "SKIPPED" {
			skip = &receipts[i]
		}
	}
	if skip == nil || skip.SuiteID != "real-usage-v1" || !strings.Contains(skip.OverrideReason, "suite file missing") {
		t.Fatalf("named SKIPPED receipt for real-usage-v1 not found: %+v", receipts)
	}
}

// TestEvalGate_SuiteSet_OneSuiteUnderThresholdFailsAggregate: the aggregate
// verdict is per-suite AND (AC1) — one suite under its threshold fails the
// whole gate even when the other passes (here code-feeding at 10/10 clean
// still misses its clean ≥ 11 bar while real-usage passes).
func TestEvalGate_SuiteSet_OneSuiteUnderThresholdFailsAggregate(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	writeSuiteN(t, filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml"), "code-feeding-v2", 10) // 10 < 11 → code-feeding FAILs
	writeSuiteN(t, filepath.Join(kb, "eval", "suites", "real-usage-v1.yaml"), "real-usage-v1", 2)

	if code := evalGate(nil); code != gateExitFail {
		t.Fatalf("eval gate (one suite under threshold) = %d, want %d (FAIL)", code, gateExitFail)
	}
}

// TestEvalGateCheck_SuiteSet_HarvestDriftGoesStale covers AC4: a fresh PASS
// per suite satisfies gate-check; appending a case to real-usage-v1 (what
// `harvest accept` does) drifts its SuiteSHA and the check demands a re-run.
func TestEvalGateCheck_SuiteSet_HarvestDriftGoesStale(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	feeding := filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml")
	realUsage := filepath.Join(kb, "eval", "suites", "real-usage-v1.yaml")
	writeSuiteN(t, feeding, "code-feeding-v2", 12)
	writeSuiteN(t, realUsage, "real-usage-v1", 2)

	if code := evalGate(nil); code != gateExitPass {
		t.Fatalf("eval gate = %d, want PASS", code)
	}
	if code := evalGateCheck(nil); code != gateExitPass {
		t.Fatalf("gate-check (fresh both) = %d, want %d (FRESH)", code, gateExitPass)
	}

	// harvest accept appends a case → suite fingerprint drifts.
	writeSuiteN(t, realUsage, "real-usage-v1", 3)
	if code := evalGateCheck(nil); code != gateExitFail {
		t.Fatalf("gate-check (post-accept drift) = %d, want %d (STALE)", code, gateExitFail)
	}
}

// TestEvalGateCheck_SuiteSet_MissingOptionalSkips: gate-check mirrors D-RG5 —
// an absent optional suite is a named skip, not an unconstrained-SHA match.
func TestEvalGateCheck_SuiteSet_MissingOptionalSkips(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, challengeJSON)
	writeSuiteN(t, filepath.Join(kb, "eval", "suites", "code-feeding-v2.yaml"), "code-feeding-v2", 12)

	if code := evalGate(nil); code != gateExitPass {
		t.Fatalf("eval gate = %d, want PASS", code)
	}
	if code := evalGateCheck(nil); code != gateExitPass {
		t.Fatalf("gate-check (optional missing) = %d, want %d", code, gateExitPass)
	}
}

func TestDefaultSuiteSet(t *testing.T) {
	set := defaultSuiteSet("/kb")
	if len(set) != 2 {
		t.Fatalf("suite set size = %d, want 2", len(set))
	}
	cf, ru := set[0], set[1]
	if cf.ID != "code-feeding-v2" || cf.Optional || cf.DeriveMinClean {
		t.Errorf("code-feeding spec = %+v: must be required with fixed thresholds", cf)
	}
	if cf.Cfg.MinClean != 11 || cf.Cfg.MaxBlockers != 0 || cf.Cfg.MinFirstTry != 0.70 {
		t.Errorf("code-feeding thresholds changed: %+v (D-RG6 locks 11/0/0.70)", cf.Cfg)
	}
	if ru.ID != "real-usage-v1" || !ru.Optional || !ru.DeriveMinClean {
		t.Errorf("real-usage spec = %+v: must be optional with derived min-clean", ru)
	}
	if ru.Cfg.MinClean != 0 {
		t.Errorf("real-usage MinClean pre-derivation = %d, want 0 (derived at load — no hard-coded case count)", ru.Cfg.MinClean)
	}
}
