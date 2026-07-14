package shortterm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNewTurnID_FormatAndStablePrefix(t *testing.T) {
	ts := time.Date(2026, 5, 14, 14, 22, 3, 0, time.UTC)
	id := NewTurnID(ts, "seed-a")

	const wantPrefix = "2026-05-14T14-22-03Z-"
	if !strings.HasPrefix(id, wantPrefix) {
		t.Fatalf("NewTurnID = %q, want prefix %q", id, wantPrefix)
	}
	if got := len(id) - len(wantPrefix); got != 6 {
		t.Errorf("hash suffix length = %d, want 6 (id=%q)", got, id)
	}
}

func TestNewTurnID_Deterministic(t *testing.T) {
	ts := time.Date(2026, 5, 14, 14, 22, 3, 0, time.UTC)
	if a, b := NewTurnID(ts, "seed"), NewTurnID(ts, "seed"); a != b {
		t.Errorf("same inputs gave different IDs: %q != %q", a, b)
	}
}

func TestNewTurnID_SeedDisambiguatesSameSecond(t *testing.T) {
	ts := time.Date(2026, 5, 14, 14, 22, 3, 0, time.UTC)
	a := NewTurnID(ts, "seed-a")
	b := NewTurnID(ts, "seed-b")
	if a == b {
		t.Errorf("distinct seeds at same second collided: %q", a)
	}
}

func TestNewTurnID_NormalisesToUTC(t *testing.T) {
	loc := time.FixedZone("EST", -5*3600)
	local := time.Date(2026, 5, 14, 9, 22, 3, 0, loc) // == 14:22:03Z
	utc := time.Date(2026, 5, 14, 14, 22, 3, 0, time.UTC)
	if a, b := NewTurnID(local, "seed"), NewTurnID(utc, "seed"); a != b {
		t.Errorf("UTC normalisation failed: %q != %q", a, b)
	}
}

func TestWrite_EmptyTurnIDIsError(t *testing.T) {
	_, err := Write(t.TempDir(), &TurnRecord{})
	if err == nil {
		t.Fatal("Write with empty TurnID: want error, got nil")
	}
	if !strings.Contains(err.Error(), "TurnID is empty") {
		t.Errorf("error = %v, want mention of empty TurnID", err)
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	kbRoot := t.TempDir()
	rec := &TurnRecord{
		TurnID:     "2026-05-14T14-22-03Z-abc123",
		TS:         "2026-05-14T14:22:03Z",
		Subcommand: "challenge",
		Scope:      []string{"backend", "universal"},
		Request:    "do the thing",
		Challenge: &ChallengeBlock{
			Verdict:      "proceed",
			Proceed:      "yes",
			Output:       "advisory",
			CiteAttempts: 2,
		},
		Performance: PerformanceBlock{ElapsedMs: 1234},
	}

	abs, err := Write(kbRoot, rec)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(kbRoot, "short-term", "turns", rec.TurnID+".yaml")
	if abs != wantPath {
		t.Errorf("returned path = %q, want %q", abs, wantPath)
	}

	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !strings.HasPrefix(string(raw), "# Olifant turn record") {
		t.Errorf("file missing self-identifying header:\n%s", raw)
	}

	var got TurnRecord
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if got.TurnID != rec.TurnID || got.Subcommand != rec.Subcommand {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
	if got.Challenge == nil || got.Challenge.Verdict != "proceed" {
		t.Errorf("challenge block lost in round-trip: %+v", got.Challenge)
	}
	if got.Performance.ElapsedMs != 1234 {
		t.Errorf("performance block lost: %+v", got.Performance)
	}
}

func TestWrite_MkdirFailsWhenRootIsFile(t *testing.T) {
	// A regular file as kbRoot makes MkdirAll(<file>/short-term/turns) fail.
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Write(f, &TurnRecord{TurnID: "2026-05-14T14-22-03Z-abc123"})
	if err == nil {
		t.Error("Write under a file-path root: want error, got nil")
	}
}

func TestValidateBlock_EnrichedRoundTripAndBackCompat(t *testing.T) {
	// Enriched block round-trips through YAML with all olifant#86 fields.
	rec := &TurnRecord{
		TurnID: "t1", TS: "2026-07-13T00:00:00Z", Subcommand: "validate",
		Request: "claim: short… | diff: short…", // display-truncated
		Validate: &ValidateBlock{
			Claim:            "the full untruncated claim text that seeds a runnable case",
			ClaudeClaimCount: 3, EvidencedClaims: 1, PartialClaims: 1,
			Cites:            []string{"D17", "AP3"},
			RetrievedSources: []string{"patterns/backend.md"},
			Proceed:          "hold", ValidateAttempts: 2,
			Verdict: "partial", Output: `{"validate":{}}`,
		},
	}
	out, err := yaml.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var got TurnRecord
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Validate.Claim != rec.Validate.Claim {
		t.Error("full Claim did not round-trip (the runnable-case seed)")
	}
	if got.Request == got.Validate.Claim {
		t.Error("Request should stay display-truncated, distinct from the full Claim")
	}
	if got.Validate.ClaudeClaimCount != 3 || got.Validate.Proceed != "hold" || got.Validate.ValidateAttempts != 2 {
		t.Errorf("enriched fields lost: %+v", got.Validate)
	}

	// Back-compat: a pre-#86 thin record (only verdict) still parses; new
	// fields read as zero.
	old := "turn_id: t0\nts: \"2026-06-01T00:00:00Z\"\nsubcommand: validate\nrequest: r\nvalidate:\n  claude_claim_count: 0\n  verdict: validated\nperformance: {}\n"
	var oldRec TurnRecord
	if err := yaml.Unmarshal([]byte(old), &oldRec); err != nil {
		t.Fatalf("pre-#86 thin record failed to parse: %v", err)
	}
	if oldRec.Validate.Verdict != "validated" || oldRec.Validate.Claim != "" || oldRec.Validate.Cites != nil {
		t.Errorf("thin record back-compat wrong: %+v", oldRec.Validate)
	}
}
