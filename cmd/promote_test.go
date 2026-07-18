package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ElatusDev/olifant/internal/promotion"
)

func promoteStateFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "promotion-state.yaml")
}

func TestPromoteDispatch(t *testing.T) {
	if code := Promote(nil); code != 2 {
		t.Errorf("Promote(nil) = %d; want 2 (missing action)", code)
	}
	if code := Promote([]string{"bogus"}); code != 2 {
		t.Errorf("Promote(bogus) = %d; want 2 (unknown action)", code)
	}
}

func TestPromoteStatusEmpty(t *testing.T) {
	if code := Promote([]string{"status", "--state", promoteStateFile(t)}); code != 0 {
		t.Errorf("promote status (empty) = %d; want 0", code)
	}
}

func TestPromoteSetAndDemote(t *testing.T) {
	path := promoteStateFile(t)
	receipts := "2026-07-14T03-29-51Z-real-usage-v1,2026-07-14T15-46-01Z-real-usage-v1"

	if code := Promote([]string{"set", "challenge", "--decision", "D250", "--receipts", receipts, "--state", path}); code != 0 {
		t.Fatalf("promote set challenge = %d; want 0", code)
	}
	st, err := promotion.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsBlocking(promotion.SurfaceChallenge) {
		t.Error("challenge not blocking after set")
	}
	if !st.HardBlocks(promotion.SurfaceChallenge, "abort") {
		t.Error("challenge should hard-block on abort after promotion")
	}

	if code := Promote([]string{"demote", "challenge", "--reason", "confirmed false block", "--state", path}); code != 0 {
		t.Fatalf("promote demote challenge = %d; want 0", code)
	}
	st, _ = promotion.Load(path)
	if st.IsBlocking(promotion.SurfaceChallenge) {
		t.Error("challenge still blocking after demote")
	}
}

func TestPromoteSetRejectsBadInput(t *testing.T) {
	path := promoteStateFile(t)
	// missing surface positional
	if code := Promote([]string{"set", "--decision", "D250", "--receipts", "a,b", "--state", path}); code != 2 {
		t.Errorf("set (no surface) = %d; want 2", code)
	}
	// only one receipt — fails the 2-PASS bar
	if code := Promote([]string{"set", "validate", "--decision", "D250", "--receipts", "just-one", "--state", path}); code != 2 {
		t.Errorf("set (1 receipt) = %d; want 2", code)
	}
	// blank decision
	if code := Promote([]string{"set", "validate", "--receipts", "a,b", "--state", path}); code != 2 {
		t.Errorf("set (blank decision) = %d; want 2", code)
	}
	// none of the rejected calls should have written a blocking state
	st, _ := promotion.Load(path)
	if st.IsBlocking(promotion.SurfaceValidate) {
		t.Error("rejected set left validate blocking")
	}
}

func TestPromoteDemoteRejectsBadInput(t *testing.T) {
	path := promoteStateFile(t)
	if code := Promote([]string{"demote", "--reason", "x", "--state", path}); code != 2 {
		t.Errorf("demote (no surface) = %d; want 2", code)
	}
	if code := Promote([]string{"demote", "challenge", "--state", path}); code != 2 {
		t.Errorf("demote (blank reason) = %d; want 2", code)
	}
}

// enforceVerdictAt: advisory state never blocks; a promoted surface blocks only
// on its hard verdict; soft/clear verdicts pass (AC6).
func TestEnforceVerdictAt(t *testing.T) {
	path := promoteStateFile(t)
	receipts := "r1,r2"

	// advisory (empty ledger): nothing blocks
	if code := enforceVerdictAt(path, "challenge", "abort", false); code != 0 {
		t.Errorf("advisory challenge abort = %d; want 0", code)
	}
	if code := enforceVerdictAt(path, "validate", "block", false); code != 0 {
		t.Errorf("advisory validate block = %d; want 0", code)
	}

	// promote both
	if err := (func() error {
		if e := promotion.Promote(path, promotion.SurfaceChallenge, "D250", []string{"r1", "r2"}, "t"); e != nil {
			return e
		}
		return promotion.Promote(path, promotion.SurfaceValidate, "D250", []string{"r1", "r2"}, "t")
	}()); err != nil {
		t.Fatalf("promote setup: %v (%s)", err, receipts)
	}

	cases := []struct {
		surface, proceed string
		want             int
	}{
		{"challenge", "abort", promotionBlockExit}, // hard → block
		{"challenge", "confirm_with_user", 0},      // soft → pass (AC6)
		{"challenge", "proceed_directly", 0},       // clear → pass
		{"challenge", "block", 0},                  // wrong enum → pass
		{"validate", "block", promotionBlockExit},  // hard → block
		{"validate", "hold", 0},                    // soft → pass (AC6)
		{"validate", "merge", 0},                   // clear → pass
		{"validate", "abort", 0},                   // wrong enum → pass
		{"challenge", "", 0},                       // unparsed → pass
	}
	for _, c := range cases {
		if got := enforceVerdictAt(path, c.surface, c.proceed, false); got != c.want {
			t.Errorf("enforceVerdictAt(%s,%q) = %d; want %d", c.surface, c.proceed, got, c.want)
		}
	}
}

// noBlock forces advisory even when promoted.
func TestEnforceVerdictNoBlockOverride(t *testing.T) {
	path := promoteStateFile(t)
	if err := promotion.Promote(path, promotion.SurfaceChallenge, "D250", []string{"r1", "r2"}, "t"); err != nil {
		t.Fatal(err)
	}
	// enforceVerdict reads the DEFAULT path, but noBlock short-circuits before any
	// read — so it returns 0 regardless of the live ledger.
	if code := enforceVerdict("challenge", "abort", true, false); code != 0 {
		t.Errorf("no-block override = %d; want 0", code)
	}
}

// An unreadable/corrupt ledger fails safe (advisory).
func TestEnforceVerdictFailsSafe(t *testing.T) {
	path := promoteStateFile(t)
	if err := os.WriteFile(path, []byte("surfaces: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := enforceVerdictAt(path, "challenge", "abort", false); code != 0 {
		t.Errorf("corrupt ledger = %d; want 0 (fail-safe)", code)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a , ,b,  ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitCSV = %v; want [a b]", got)
	}
	if splitCSV("   ") != nil {
		t.Error("splitCSV(blank) should be nil")
	}
}

func writeReconcileFixtures(t *testing.T, blocking bool) (statePath, reactionsPath string) {
	t.Helper()
	dir := t.TempDir()
	statePath = filepath.Join(dir, "promotion-state.yaml")
	if blocking {
		if err := promotion.Promote(statePath, "challenge", "D250", []string{"r1", "r2"}, "2026-07-14T00:00:00Z"); err != nil {
			t.Fatalf("promote: %v", err)
		}
	}
	reactionsPath = filepath.Join(dir, "reactions.jsonl")
	line := `{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject","note":"false block"}` + "\n"
	if err := os.WriteFile(reactionsPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write reactions: %v", err)
	}
	return statePath, reactionsPath
}

func TestPromoteReconcileDemotes(t *testing.T) {
	statePath, reactionsPath := writeReconcileFixtures(t, true)
	if code := Promote([]string{"reconcile", "--state", statePath, "--reactions", reactionsPath}); code != 0 {
		t.Fatalf("promote reconcile = %d; want 0", code)
	}
	st, err := promotion.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.IsBlocking("challenge") {
		t.Fatal("challenge should be advisory after reconcile")
	}
	sf := st.Surfaces["challenge"]
	if sf.Demotion == nil || sf.Demotion.Reason == "" {
		t.Fatalf("demotion must record the reaction as reason, got %+v", sf)
	}
}

func TestPromoteReconcileDryRunWritesNothing(t *testing.T) {
	statePath, reactionsPath := writeReconcileFixtures(t, true)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if code := Promote([]string{"reconcile", "--dry-run", "-v", "--state", statePath, "--reactions", reactionsPath}); code != 0 {
		t.Fatalf("promote reconcile --dry-run = %d; want 0", code)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run must not modify the ledger")
	}
}

func TestPromoteReconcileAbsentReactionsNoOp(t *testing.T) {
	statePath := promoteStateFile(t)
	if code := Promote([]string{"reconcile", "--state", statePath, "--reactions", filepath.Join(t.TempDir(), "nope.jsonl")}); code != 0 {
		t.Fatalf("absent reactions should be a clean no-op exit 0, got %d", code)
	}
}

func TestPromoteReconcileCorruptStateErrors(t *testing.T) {
	_, reactionsPath := writeReconcileFixtures(t, false)
	statePath := promoteStateFile(t)
	if err := os.WriteFile(statePath, []byte(":\nnot yaml : ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Promote([]string{"reconcile", "--state", statePath, "--reactions", reactionsPath}); code != 2 {
		t.Fatalf("corrupt state should error (2), got %d", code)
	}
}
