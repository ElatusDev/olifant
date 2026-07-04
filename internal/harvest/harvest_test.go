package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixtures(t *testing.T) (reactionsPath, kbRoot string) {
	t.Helper()
	dir := t.TempDir()
	kbRoot = filepath.Join(dir, "kb")
	turns := filepath.Join(kbRoot, "short-term", "turns")
	if err := os.MkdirAll(turns, 0o755); err != nil {
		t.Fatal(err)
	}
	turn := func(id, sub, req, block string) {
		body := "turn_id: " + id + "\nts: \"2026-07-01T00:00:00Z\"\nsubcommand: " + sub +
			"\nscope:\n    - backend\nrequest: " + req + "\n" + block +
			"performance:\n    elapsed_ms: 1\n"
		if err := os.WriteFile(filepath.Join(turns, id+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	turn("t-accept", "challenge", "add a tenant-scoped invoice entity",
		"challenge:\n    verdict: VALID\n    proceed: proceed\n    output: x\n    cite_attempts: 1\n")
	turn("t-reject", "validate", "claim truncated…",
		"validate:\n    claude_claim_count: 1\n    evidenced_claims: 0\n    verdict: failed\n")
	turn("t-retro-live", "challenge", "question with both lines",
		"challenge:\n    verdict: VALID\n    proceed: proceed\n    output: x\n    cite_attempts: 1\n")
	turn("t-retracted", "challenge", "was mislabeled",
		"challenge:\n    verdict: VALID\n    proceed: proceed\n    output: x\n    cite_attempts: 1\n")

	lines := []string{
		`{"turn_id":"t-accept","ts":"2026-07-01T01:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"accept","unresolved_cites":["decisions/log.md#D9","tenant scoping"]}`,
		`{"turn_id":"t-reject","ts":"2026-07-01T02:00:00Z","subcommand":"validate","verdict":"failed","reaction":"reject","note":"claim later proven"}`,
		`{"turn_id":"t-retro-live","ts":"2026-07-01T03:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"reject"}`,
		`{"turn_id":"t-retro-live","ts":"2026-07-02T00:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"accept","phase":"retro","label":"confirmed","note":"hindsight: verdict held"}`,
		`{"turn_id":"t-retracted","ts":"2026-07-02T01:00:00Z","subcommand":"challenge","verdict":"VALID","reaction":"accept","phase":"retro","label":"confirmed"}`,
		`{"turn_id":"t-retracted","ts":"2026-07-02T02:00:00Z","subcommand":"challenge","verdict":"n/a","reaction":"none","phase":"retro","label":"retracted","note":"mislabel"}`,
		`{"turn_id":"none","ts":"2026-07-01T04:00:00Z","subcommand":"challenge","verdict":"SKIPPED","reaction":"none"}`,
		`not json at all`,
	}
	reactionsPath = filepath.Join(dir, "reactions.jsonl")
	if err := os.WriteFile(reactionsPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return reactionsPath, kbRoot
}

func TestLoadReactions_TolerantParse(t *testing.T) {
	rp, _ := writeFixtures(t)
	rs, skipped, err := LoadReactions(rp)
	if err != nil {
		t.Fatalf("LoadReactions: %v", err)
	}
	if len(rs) != 7 || len(skipped) != 1 {
		t.Errorf("parsed=%d skipped=%d, want 7/1", len(rs), len(skipped))
	}
}

func TestEffective_D_OL2PrecedenceAndRetraction(t *testing.T) {
	rp, _ := writeFixtures(t)
	rs, _, _ := LoadReactions(rp)
	eff, unjoinable := Effective(rs)

	if unjoinable != 1 {
		t.Errorf("unjoinable = %d, want 1", unjoinable)
	}
	if len(eff) != 3 { // t-accept, t-reject, t-retro-live; t-retracted removed
		t.Fatalf("effective = %d (%v), want 3", len(eff), eff)
	}
	if got := eff["t-retro-live"]; got.Phase != "retro" || got.Label != "confirmed" {
		t.Errorf("retro precedence: got %+v", got)
	}
	if _, ok := eff["t-retracted"]; ok {
		t.Error("retracted turn must be removed")
	}
}

func TestJoinAndClassify_BucketsAndDedup(t *testing.T) {
	rp, kb := writeFixtures(t)
	rs, _, _ := LoadReactions(rp)
	eff, _ := Effective(rs)
	signals := Join(eff, kb)

	props := Classify(signals, map[string]bool{})
	byKind := map[Kind]int{}
	for _, p := range props {
		byKind[p.Kind]++
	}
	// t-accept → eval-case + corpus-gap(path cite) + dict-term("tenant scoping")
	// t-reject → investigate; t-retro-live → eval-case
	if byKind[KindEvalCase] != 2 || byKind[KindInvestigate] != 1 ||
		byKind[KindCorpusGap] != 1 || byKind[KindDictTerm] != 1 {
		t.Errorf("buckets = %+v", byKind)
	}

	// Cursor dedup: everything harvested → nothing proposed.
	harvested := map[string]bool{}
	for _, p := range props {
		harvested[p.TurnID] = true
	}
	if again := Classify(signals, harvested); len(again) != 0 {
		t.Errorf("dedup: %d proposals after full cursor, want 0", len(again))
	}
}

func TestClassify_TruncatedRequestBecomesPointer(t *testing.T) {
	sig := []Signal{{Reaction: Reaction{TurnID: "x", Subcommand: "challenge", Reaction: "accept"},
		Turn: nil}}
	props := Classify(sig, nil)
	if len(props) != 1 || props[0].Kind != KindEvalPointer {
		t.Errorf("nil turn should yield pointer: %+v", props)
	}
}

func TestReport_DeterministicAndProposeOnly(t *testing.T) {
	rp, kb := writeFixtures(t)
	rs, _, _ := LoadReactions(rp)
	eff, unjoin := Effective(rs)
	signals := Join(eff, kb)
	props := Classify(signals, nil)

	out := t.TempDir()
	p1, err := WriteReport(out, signals, props, unjoin, nil)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b1, _ := os.ReadFile(p1)
	p2, _ := WriteReport(out, signals, props, unjoin, nil)
	b2, _ := os.ReadFile(p2)
	if p1 != p2 || string(b1) != string(b2) {
		t.Error("report not deterministic across runs")
	}
	if !strings.Contains(string(b1), "PROPOSE-ONLY") {
		t.Error("report must declare propose-only")
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harvest", "cursor")
	empty, err := LoadCursor(path)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty cursor: %v %v", empty, err)
	}
	props := []Proposal{{TurnID: "b"}, {TurnID: "a"}}
	if err := SaveCursor(path, map[string]bool{"c": true}, props); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	got, err := LoadCursor(path)
	if err != nil || !got["a"] || !got["b"] || !got["c"] {
		t.Errorf("cursor round-trip: %v %v", got, err)
	}
}
