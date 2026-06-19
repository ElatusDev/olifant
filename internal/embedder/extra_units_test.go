package embedder

import (
	"strings"
	"testing"
)

func TestAnyOverlap(t *testing.T) {
	if !anyOverlap([]string{"a", "b"}, []string{"x", "b"}) {
		t.Error("overlapping slices should report true")
	}
	if anyOverlap([]string{"a", "b"}, []string{"x", "y"}) {
		t.Error("disjoint slices should report false")
	}
	if anyOverlap(nil, []string{"a"}) {
		t.Error("empty a should report false")
	}
}

func TestMax1(t *testing.T) {
	if max1(0) != 1 || max1(-5) != 1 {
		t.Error("max1 should floor at 1")
	}
	if max1(7) != 7 {
		t.Error("max1 should pass through values >= 1")
	}
}

func TestTruncStr(t *testing.T) {
	if got := truncStr("short", 10); got != "short" {
		t.Errorf("under length = %q", got)
	}
	got := truncStr("abcdef", 3)
	if got != "abc…" {
		t.Errorf("over length = %q, want abc…", got)
	}
}

func TestParaphraseError_Error(t *testing.T) {
	if got := (&ParaphraseError{Kind: FailTimeout}).Error(); got != string(FailTimeout) {
		t.Errorf("no-detail = %q, want %q", got, FailTimeout)
	}
	withDetail := (&ParaphraseError{Kind: FailSubprocess, Detail: "exit 1"}).Error()
	if withDetail != string(FailSubprocess)+": exit 1" {
		t.Errorf("with-detail = %q", withDetail)
	}
}

func TestMiningStats_HumanString(t *testing.T) {
	st := MiningStats{
		AnchorCount:  10,
		TripleCount:  7,
		DroppedCount: 3,
		RelaxedCount: 1,
		ByScope:      map[string]int{"backend": 4, "webapp": 3},
	}
	out := st.HumanString()
	if !strings.Contains(out, "anchors=10") || !strings.Contains(out, "triples=7") {
		t.Errorf("summary line missing counts:\n%s", out)
	}
	// Per-scope lines are sorted: backend before webapp.
	bi := strings.Index(out, "backend")
	wi := strings.Index(out, "webapp")
	if bi < 0 || wi < 0 || bi > wi {
		t.Errorf("per-scope not sorted ascending:\n%s", out)
	}
}
