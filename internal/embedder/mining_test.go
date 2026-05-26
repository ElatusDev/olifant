package embedder

import (
	"testing"
)

// build a Sentence quickly for table-driven tests.
func mk(id, scope, role string, concerns ...string) Sentence {
	s := Sentence{ID: id, Text: id, Scope: scope, SemanticRole: role}
	s.Concerns = append(s.Concerns, concerns...)
	return s
}

func TestMine_SameScopeDifferentRoleHardestNegative(t *testing.T) {
	corpus := []Sentence{
		mk("A", "backend", "constraint", "security", "testing"),
		// Same scope, different role; shares 2/2 concerns with anchor -> highest cosine.
		mk("B", "backend", "definition", "security", "testing"),
		// Same scope, different role; shares only 1 concern.
		mk("C", "backend", "example", "security"),
		// Same scope, SAME role -> not a candidate.
		mk("D", "backend", "constraint", "security", "testing"),
		// Different scope -> not a candidate at Step 1.
		mk("E", "webapp", "definition", "security", "testing"),
	}
	triples := Mine(corpus)
	if got, want := len(triples), 5; got != want {
		t.Fatalf("triples=%d, want %d", got, want)
	}
	// Anchor A: expect B as negative (highest cosine, same scope, different role).
	var aRow *Triple
	for i := range triples {
		if triples[i].AnchorID == "A" {
			aRow = &triples[i]
			break
		}
	}
	if aRow == nil {
		t.Fatal("anchor A produced no triple")
	}
	if aRow.NegativeID != "B" {
		t.Errorf("A's negative=%q, want B (highest-cosine same-scope-different-role)", aRow.NegativeID)
	}
	if aRow.Relaxed {
		t.Errorf("A should not be relaxed (same-scope candidate exists)")
	}
}

func TestMine_RelaxesWhenSameScopeAllSameRole(t *testing.T) {
	// All backend anchors share the SAME role -> Step 1 yields nothing.
	corpus := []Sentence{
		mk("A", "backend", "constraint", "security"),
		mk("B", "backend", "constraint", "testing"),
		mk("X", "webapp", "definition", "security"),
	}
	triples := Mine(corpus)
	var aRow *Triple
	for i := range triples {
		if triples[i].AnchorID == "A" {
			aRow = &triples[i]
		}
	}
	if aRow == nil {
		t.Fatal("A produced no triple")
	}
	if aRow.NegativeID != "X" {
		t.Errorf("A's negative=%q, want X (cross-scope fallback)", aRow.NegativeID)
	}
	if !aRow.Relaxed {
		t.Errorf("A should be Relaxed=true (no same-scope candidate)")
	}
}

func TestMine_DropsAnchorWithNoCandidateRoleDiff(t *testing.T) {
	// Every sentence shares the SAME role across all scopes -> no candidate
	// has different role -> anchor must be dropped.
	corpus := []Sentence{
		mk("A", "backend", "constraint"),
		mk("B", "webapp", "constraint"),
	}
	triples := Mine(corpus)
	if len(triples) != 0 {
		t.Errorf("expected 0 triples (no different-role candidate), got %d", len(triples))
	}
}

func TestMine_DeterministicTieBreak(t *testing.T) {
	// Two equally-cosine candidates; lex-smaller ID must win.
	corpus := []Sentence{
		mk("A", "backend", "constraint", "security"),
		mk("Z", "backend", "definition", "security"), // tied
		mk("B", "backend", "definition", "security"), // tied; B<Z lex
	}
	for i := 0; i < 5; i++ {
		triples := Mine(corpus)
		var aRow *Triple
		for j := range triples {
			if triples[j].AnchorID == "A" {
				aRow = &triples[j]
			}
		}
		if aRow == nil {
			t.Fatal("A missing")
		}
		if aRow.NegativeID != "B" {
			t.Errorf("iter %d: tiebreak picked %q, want B (lex-smaller)", i, aRow.NegativeID)
		}
	}
}

func TestSummarise(t *testing.T) {
	corpus := []Sentence{
		mk("A", "backend", "constraint", "security"),
		mk("B", "backend", "definition", "security"),
	}
	triples := Mine(corpus)
	st := Summarise(corpus, triples)
	if st.AnchorCount != 2 || st.TripleCount != 2 {
		t.Errorf("counts: %+v", st)
	}
	if st.ByScope["backend"] != 2 {
		t.Errorf("ByScope backend = %d, want 2", st.ByScope["backend"])
	}
}
