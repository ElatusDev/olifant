package promotion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promoteAt writes a ledger with surface blocking, promoted at the given ts.
func promoteAt(t *testing.T, statePath, surface, at string) {
	t.Helper()
	if err := Promote(statePath, surface, "D250", []string{"r1", "r2"}, at); err != nil {
		t.Fatalf("promote: %v", err)
	}
}

func writeReactions(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, "reactions.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write reactions: %v", err)
	}
	return p
}

func reconcileT(t *testing.T, cfg ReconcileConfig) *ReconcileReport {
	t.Helper()
	rep, err := Reconcile(cfg)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return rep
}

func TestReconcile_LiveRejectHardVerdictDemotes(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject","note":"blocked a valid request"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "2026-07-18T00:00:00Z"})
	if len(rep.Demoted) != 1 || rep.Demoted[0].Action != ActionDemoted {
		t.Fatalf("want 1 demoted, got %+v", rep.Demoted)
	}
	st, _ := Load(state)
	if st.IsBlocking(SurfaceChallenge) {
		t.Fatal("challenge should be advisory after reconcile")
	}
	sf := st.Surfaces[SurfaceChallenge]
	if sf.Demotion == nil || !strings.Contains(sf.Demotion.Reason, "confirmed false block") ||
		!strings.Contains(sf.Demotion.Reason, "2026-07-15T10:00:00Z") ||
		!strings.Contains(sf.Demotion.Reason, "verdict=INVALID") ||
		!strings.Contains(sf.Demotion.Reason, "blocked a valid request") {
		t.Fatalf("demotion reason must name the reaction, got %+v", sf.Demotion)
	}
	if sf.Demotion.At != "2026-07-18T00:00:00Z" {
		t.Fatalf("demotion At should be injected Now, got %s", sf.Demotion.At)
	}
}

func TestReconcile_RetroWrongDemotes(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceValidate, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown-2","ts":"2026-07-16T09:00:00Z","subcommand":"validate","verdict":"failed","reaction":"none","phase":"retro","label":"wrong"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 1 || rep.Demoted[0].Surface != SurfaceValidate {
		t.Fatalf("want validate demoted, got %+v", rep.Demoted)
	}
}

func TestReconcile_SoftVerdictNeverDemotes(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"NEEDS_CLARIFICATION","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("soft verdict must not demote: %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "not the hard block") {
		t.Fatalf("want a not-the-hard-block skip, got %+v", rep.Skipped)
	}
}

func TestReconcile_TSGuardKeepsRePromotionSafe(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	// The false-block signal predates the (re-)promotion: it must not demote.
	promoteAt(t, state, SurfaceChallenge, "2026-07-16T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("stale signal must not demote after re-promotion: %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "predates promotion") {
		t.Fatalf("want a predates-promotion skip, got %+v", rep.Skipped)
	}
	if st, _ := Load(state); !st.IsBlocking(SurfaceChallenge) {
		t.Fatal("surface must stay blocking")
	}
}

func TestReconcile_RetractedVetoesCluster(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"t1","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`,
		`{"turn_id":"t1","ts":"2026-07-16T10:00:00Z","subcommand":"challenge","phase":"retro","label":"retracted"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("retracted cluster must not demote: %+v", rep.Demoted)
	}
	for _, s := range rep.Skipped {
		if !strings.Contains(s.Reason, "retracted") {
			t.Fatalf("cluster lines should skip as retracted, got %+v", s)
		}
	}
}

func TestReconcile_RetroConfirmedOverridesLiveReject(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"t2","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`,
		`{"turn_id":"t2","ts":"2026-07-16T10:00:00Z","subcommand":"challenge","verdict":"INVALID","phase":"retro","label":"confirmed"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("retro confirmed must override the live reject: %+v", rep.Demoted)
	}
	var reasons []string
	for _, s := range rep.Skipped {
		reasons = append(reasons, s.Reason)
	}
	joined := strings.Join(reasons, " | ")
	if !strings.Contains(joined, "superseded") || !strings.Contains(joined, "label=confirmed") {
		t.Fatalf("want superseded + label=confirmed skips, got %s", joined)
	}
}

func TestReconcile_UnknownIDLinesEvaluatePerLine(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	// Two "unknown" lines must NOT reduce into one cluster: the reject demotes,
	// the accept is independently reported.
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`,
		`{"turn_id":"unknown","ts":"2026-07-15T11:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"accept"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 1 {
		t.Fatalf("want the reject line to demote, got %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "label=confirmed") {
		t.Fatalf("want the accept line evaluated per-line, got %+v", rep.Skipped)
	}
}

func TestReconcile_DateOnlyTS(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-18","subcommand":"challenge","verdict":"OUT_OF_SCOPE","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 1 {
		t.Fatalf("date-only ts newer than promotion must demote, got %+v", rep.Skipped)
	}
}

func TestReconcile_UnparseableTSKeepsEnforcement(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"yesterday-ish","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("unparseable ts must not demote: %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "unparseable ts") {
		t.Fatalf("want unparseable-ts skip, got %+v", rep.Skipped)
	}
	if st, _ := Load(state); !st.IsBlocking(SurfaceChallenge) {
		t.Fatal("surface must stay blocking")
	}
}

func TestReconcile_AdvisorySurfaceNoOp(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("advisory surface must be a no-op: %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "advisory") {
		t.Fatalf("want advisory skip, got %+v", rep.Skipped)
	}
}

func TestReconcile_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	before, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, DryRun: true, Now: "n"})
	if len(rep.Demoted) != 1 || rep.Demoted[0].Action != ActionWouldDemote {
		t.Fatalf("want one would-demote, got %+v", rep.Demoted)
	}
	after, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run must not modify the ledger")
	}
}

func TestReconcile_SecondSignalSameSurfaceSkips(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`,
		`{"turn_id":"unknown","ts":"2026-07-15T11:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 1 {
		t.Fatalf("want exactly one demotion, got %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "already demoted") {
		t.Fatalf("want already-demoted skip, got %+v", rep.Skipped)
	}
}

func TestReconcile_AbsentReactionsIsCleanNoOp(t *testing.T) {
	dir := t.TempDir()
	rep := reconcileT(t, ReconcileConfig{
		ReactionsPath: filepath.Join(dir, "nope.jsonl"),
		StatePath:     filepath.Join(dir, "state.yaml"), Now: "n"})
	if !rep.ReactionsAbsent || len(rep.Demoted)+len(rep.Skipped) != 0 {
		t.Fatalf("absent reactions must be a clean no-op, got %+v", rep)
	}
}

func TestReconcile_CorruptStateErrors(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	if err := os.WriteFile(state, []byte(":\nnot yaml : ["), 0o644); err != nil {
		t.Fatal(err)
	}
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","verdict":"INVALID","reaction":"reject"}`)

	if _, err := Reconcile(ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"}); err == nil {
		t.Fatal("corrupt state must be an error, never guessed past")
	}
}

func TestReconcile_MalformedLinesReported(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	reactions := writeReactions(t, dir,
		`{not json`,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"prompt build","verdict":"","reaction":"accept"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Malformed) != 1 {
		t.Fatalf("want 1 malformed line, got %v", rep.Malformed)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "not a promotable verdict surface") {
		t.Fatalf("want not-a-surface skip, got %+v", rep.Skipped)
	}
}

func TestReconcile_TurnJoinSuppliesProceed(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceValidate, "2026-07-14T00:00:00Z")
	// The reaction line has NO verdict; the joined turn record carries the
	// hard proceed — the join, not the verdict map, supplies the evidence.
	kbRoot := filepath.Join(dir, "kb")
	turnDir := filepath.Join(kbRoot, "short-term", "turns")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	turnYAML := "validate:\n  verdict: failed\n  proceed: block\n"
	if err := os.WriteFile(filepath.Join(turnDir, "tv1.yaml"), []byte(turnYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	reactions := writeReactions(t, dir,
		`{"turn_id":"tv1","ts":"2026-07-15T10:00:00Z","subcommand":"validate","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, KBRoot: kbRoot, Now: "n"})
	if len(rep.Demoted) != 1 || rep.Demoted[0].Verdict != "failed" {
		t.Fatalf("turn join should supply proceed+verdict, got demoted=%+v skipped=%+v", rep.Demoted, rep.Skipped)
	}
}

func TestReconcile_NoVerdictEvidenceKeepsEnforcement(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.yaml")
	promoteAt(t, state, SurfaceChallenge, "2026-07-14T00:00:00Z")
	reactions := writeReactions(t, dir,
		`{"turn_id":"unknown","ts":"2026-07-15T10:00:00Z","subcommand":"challenge","reaction":"reject"}`)

	rep := reconcileT(t, ReconcileConfig{ReactionsPath: reactions, StatePath: state, Now: "n"})
	if len(rep.Demoted) != 0 {
		t.Fatalf("no verdict evidence must not demote: %+v", rep.Demoted)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "no verdict evidence") {
		t.Fatalf("want no-verdict-evidence skip, got %+v", rep.Skipped)
	}
}
