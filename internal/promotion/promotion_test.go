package promotion

import (
	"os"
	"path/filepath"
	"testing"
)

func tmpPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "promotion-state.yaml")
}

// An absent ledger reads as empty and every surface is advisory — the fail-safe
// default a fresh machine must have (never block until promoted).
func TestLoadMissingIsAdvisory(t *testing.T) {
	s, err := Load(tmpPath(t))
	if err != nil {
		t.Fatalf("Load(absent) error = %v; want nil", err)
	}
	for _, sf := range Surfaces {
		if s.IsBlocking(sf) {
			t.Errorf("%s blocking on empty state; want advisory", sf)
		}
		if got := s.StatusOf(sf); got != StatusAdvisory {
			t.Errorf("StatusOf(%s) = %q; want advisory", sf, got)
		}
		if s.HardBlocks(sf, "abort") || s.HardBlocks(sf, "block") {
			t.Errorf("%s HardBlocks on empty state; want false", sf)
		}
	}
}

// Promote records the decision + receipts and flips to blocking; the hard
// verdict then blocks while soft/clear verdicts pass — per surface.
func TestPromoteThenHardBlocks(t *testing.T) {
	path := tmpPath(t)
	if err := Promote(path, SurfaceChallenge, "D250", []string{"r1", "r2"}, "2026-07-14T15:26:13Z"); err != nil {
		t.Fatalf("Promote challenge: %v", err)
	}
	if err := Promote(path, SurfaceValidate, "D250", []string{"r1", "r2"}, "2026-07-14T15:26:13Z"); err != nil {
		t.Fatalf("Promote validate: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		surface string
		proceed string
		block   bool
	}{
		// challenge: abort blocks; confirm_with_user / proceed_directly pass
		{SurfaceChallenge, "abort", true},
		{SurfaceChallenge, "confirm_with_user", false},
		{SurfaceChallenge, "proceed_directly", false},
		{SurfaceChallenge, "block", false}, // wrong enum for challenge — must not block
		// validate: block blocks; hold / merge pass
		{SurfaceValidate, "block", true},
		{SurfaceValidate, "hold", false},
		{SurfaceValidate, "merge", false},
		{SurfaceValidate, "abort", false}, // wrong enum for validate — must not block
		// empty proceed (parse failure) never blocks
		{SurfaceChallenge, "", false},
		{SurfaceValidate, "", false},
	}
	for _, c := range cases {
		if got := s.HardBlocks(c.surface, c.proceed); got != c.block {
			t.Errorf("HardBlocks(%s, %q) = %v; want %v", c.surface, c.proceed, got, c.block)
		}
	}

	sf := s.Surfaces[SurfaceChallenge]
	if sf.Decision != "D250" || len(sf.Receipts) != 2 || sf.PromotedAt == "" {
		t.Errorf("promotion record incomplete: %+v", sf)
	}
}

// Demote flips a promoted surface back to advisory, records the reason, and the
// hard verdict stops blocking — the wired §7 demotion path.
func TestDemoteReverts(t *testing.T) {
	path := tmpPath(t)
	if err := Promote(path, SurfaceValidate, "D250", []string{"r1", "r2"}, "t0"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := Demote(path, SurfaceValidate, "confirmed false block on PR#123", "t1"); err != nil {
		t.Fatalf("Demote: %v", err)
	}
	s, _ := Load(path)
	if s.IsBlocking(SurfaceValidate) {
		t.Error("validate still blocking after demote")
	}
	if s.HardBlocks(SurfaceValidate, "block") {
		t.Error("validate hard-blocks after demote; want advisory")
	}
	d := s.Surfaces[SurfaceValidate].Demotion
	if d == nil || d.Reason == "" || d.At != "t1" {
		t.Errorf("demotion not recorded: %+v", d)
	}
}

// Demote on an absent/advisory surface is idempotent (records without error).
func TestDemoteIdempotentOnAdvisory(t *testing.T) {
	path := tmpPath(t)
	if err := Demote(path, SurfaceChallenge, "precautionary", "t0"); err != nil {
		t.Fatalf("Demote(advisory): %v", err)
	}
	s, _ := Load(path)
	if s.StatusOf(SurfaceChallenge) != StatusAdvisory {
		t.Error("challenge not advisory after idempotent demote")
	}
}

func TestPromoteRejectsBadInput(t *testing.T) {
	path := tmpPath(t)
	if err := Promote(path, "nonsense", "D250", []string{"r1", "r2"}, "t"); err == nil {
		t.Error("Promote(unknown surface) = nil; want error")
	}
	if err := Promote(path, SurfaceChallenge, "", []string{"r1", "r2"}, "t"); err == nil {
		t.Error("Promote(blank decision) = nil; want error (criterion 4)")
	}
	if err := Promote(path, SurfaceChallenge, "D250", []string{"r1"}, "t"); err == nil {
		t.Error("Promote(1 receipt) = nil; want error (2-PASS bar)")
	}
	// none of the rejected calls should have created a blocking state
	s, _ := Load(path)
	if s.IsBlocking(SurfaceChallenge) {
		t.Error("rejected promotion left surface blocking")
	}
}

func TestDemoteRejectsBadInput(t *testing.T) {
	path := tmpPath(t)
	if err := Demote(path, "nonsense", "reason", "t"); err == nil {
		t.Error("Demote(unknown surface) = nil; want error")
	}
	if err := Demote(path, SurfaceChallenge, "", "t"); err == nil {
		t.Error("Demote(blank reason) = nil; want error")
	}
}

// A corrupt ledger surfaces a parse error rather than silently reading as empty
// (which would mask a real problem).
func TestLoadCorruptErrors(t *testing.T) {
	path := tmpPath(t)
	if err := os.WriteFile(path, []byte("surfaces: [not-a-map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load(corrupt) = nil; want parse error")
	}
}

func TestKnown(t *testing.T) {
	if !Known(SurfaceChallenge) || !Known(SurfaceValidate) {
		t.Error("known surfaces reported unknown")
	}
	if Known("digest") {
		t.Error("digest reported known; not a promotable surface")
	}
}

// Save round-trips through Load with the record intact.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := tmpPath(t)
	in := &State{Surfaces: map[string]*Surface{
		SurfaceChallenge: {Status: StatusBlocking, Decision: "D250", Receipts: []string{"a", "b"}, PromotedAt: "t"},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !out.IsBlocking(SurfaceChallenge) || out.Surfaces[SurfaceChallenge].Decision != "D250" {
		t.Errorf("round-trip lost data: %+v", out.Surfaces[SurfaceChallenge])
	}
}
