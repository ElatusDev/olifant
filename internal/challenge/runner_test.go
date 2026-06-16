package challenge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// h builds a minimal hit fixture for the selectTopWithFMReserve tests.
// Distance is the only ranking signal needed; Scope drives FM vs non-FM
// classification.
func h(distance float32, scope, label string) retrievedHit {
	return retrievedHit{Distance: distance, Scope: scope, Meta: map[string]interface{}{"source": label}}
}

func TestHitSummaries(t *testing.T) {
	if got := hitSummaries(nil); got != nil {
		t.Fatalf("hitSummaries(nil) = %v, want nil", got)
	}

	long := strings.Repeat("a", docSnippetMaxChars+50)
	multibyte := strings.Repeat("é", docSnippetMaxChars+10) // 2-byte runes
	hits := []retrievedHit{
		{Doc: "short doc", Distance: 0.05, Scope: "mobile/code",
			Meta: map[string]interface{}{"source": "SettingsScreen.tsx", "artifact_id": "abc123"}},
		{Doc: long, Distance: 0.23, Scope: "universal/failure_modes",
			Meta: map[string]interface{}{"source": "FM6"}},
		{Doc: multibyte, Distance: 0.30, Scope: "backend/code", Meta: map[string]interface{}{}},
	}
	got := hitSummaries(hits)
	if len(got) != 3 {
		t.Fatalf("len(got)=%d want 3", len(got))
	}

	// Rank is 1-based and preserves order.
	for i, hs := range got {
		if hs.Rank != i+1 {
			t.Errorf("hit %d rank=%d want %d", i, hs.Rank, i+1)
		}
	}
	// Passthrough + Meta extraction.
	if got[0].Distance != 0.05 || got[0].Scope != "mobile/code" {
		t.Errorf("hit0 distance/scope = %v/%q", got[0].Distance, got[0].Scope)
	}
	if got[0].Source != "SettingsScreen.tsx" || got[0].ArtifactID != "abc123" {
		t.Errorf("hit0 source/artifact = %q/%q", got[0].Source, got[0].ArtifactID)
	}
	if got[0].DocSnippet != "short doc" {
		t.Errorf("hit0 snippet = %q want unchanged", got[0].DocSnippet)
	}
	// Missing artifact_id stays empty (omitempty); source present.
	if got[1].Source != "FM6" || got[1].ArtifactID != "" {
		t.Errorf("hit1 source/artifact = %q/%q", got[1].Source, got[1].ArtifactID)
	}
	// Missing source AND artifact_id → both empty.
	if got[2].Source != "" || got[2].ArtifactID != "" {
		t.Errorf("hit2 source/artifact should be empty, got %q/%q", got[2].Source, got[2].ArtifactID)
	}
	// ASCII truncation: exactly docSnippetMaxChars runes + ellipsis marker.
	if r := []rune(got[1].DocSnippet); len(r) != docSnippetMaxChars+1 || r[docSnippetMaxChars] != '…' {
		t.Errorf("hit1 snippet rune len=%d want %d+ellipsis", len(r), docSnippetMaxChars)
	}
	// Multi-byte truncation is rune-safe (valid UTF-8, correct rune count).
	if r := []rune(got[2].DocSnippet); len(r) != docSnippetMaxChars+1 {
		t.Errorf("hit2 multibyte snippet rune len=%d want %d+1", len(r), docSnippetMaxChars)
	}
	if !utf8.ValidString(got[2].DocSnippet) {
		t.Errorf("hit2 snippet is not valid UTF-8")
	}
}

func TestSelectTopWithFMReserve_codeHeavyMobile(t *testing.T) {
	// Mirror the 2026-05-18 22:32Z eval case 3 retrieval pool: 8
	// mobile/code candidates all closer than the failure_modes
	// universal hits. Pre-fix, the FM hits got crowded out 0/8.
	hits := []retrievedHit{
		h(0.0568, "mobile/code", "SettingsScreen.tsx"),
		h(0.0706, "mobile/code", "PaymentHistoryScreen.tsx"),
		h(0.0711, "mobile/code", "CollaboratorsAnalyticsScreen.tsx"),
		h(0.0716, "mobile/code", "NotificationPropagator.tsx"),
		h(0.0717, "mobile/code", "StudentsAnalyticsScreen.tsx"),
		h(0.0740, "mobile/code", "BiometricScreen.tsx"),
		h(0.0752, "mobile/code", "EmployeeListScreen.tsx"),
		h(0.0754, "mobile/code", "CourseDetailScreen.tsx"),
		h(0.2343, "universal/failure_modes", "FM6"),
		h(0.2875, "universal/failure_modes", "FM4"),
	}
	out := selectTopWithFMReserve(hits, 8, 2)
	if len(out) != 8 {
		t.Fatalf("len(out)=%d want 8", len(out))
	}
	var fmCount int
	for _, x := range out {
		if x.Scope == "universal/failure_modes" {
			fmCount++
		}
	}
	if fmCount != 2 {
		t.Errorf("fmCount=%d want 2 (reserve floor)", fmCount)
	}
	// Sort invariant: final ordering by ascending distance.
	for i := 1; i < len(out); i++ {
		if out[i].Distance < out[i-1].Distance {
			t.Errorf("output not distance-sorted at index %d: %.4f < %.4f", i, out[i].Distance, out[i-1].Distance)
		}
	}
}

func TestSelectTopWithFMReserve_belowTopNReturnsAll(t *testing.T) {
	hits := []retrievedHit{
		h(0.10, "mobile/code", "a"),
		h(0.20, "universal/failure_modes", "FM1"),
	}
	out := selectTopWithFMReserve(hits, 8, 2)
	if len(out) != 2 {
		t.Errorf("len(out)=%d want 2 (passthrough)", len(out))
	}
}

func TestSelectTopWithFMReserve_partialReserveFill(t *testing.T) {
	// Only 1 FM hit available; reserve asks for 2. Should still fill
	// the remaining slots from non-FM candidates without error.
	hits := []retrievedHit{
		h(0.10, "mobile/code", "a"),
		h(0.11, "mobile/code", "b"),
		h(0.12, "mobile/code", "c"),
		h(0.50, "universal/failure_modes", "FM1"),
	}
	out := selectTopWithFMReserve(hits, 3, 2)
	if len(out) != 3 {
		t.Fatalf("len(out)=%d want 3", len(out))
	}
	var fm int
	for _, x := range out {
		if x.Scope == "universal/failure_modes" {
			fm++
		}
	}
	if fm != 1 {
		t.Errorf("fmCount=%d want 1 (only one FM available)", fm)
	}
}

func TestSelectTopWithFMReserve_zeroReserve(t *testing.T) {
	hits := []retrievedHit{
		h(0.10, "mobile/code", "a"),
		h(0.11, "mobile/code", "b"),
		h(0.50, "universal/failure_modes", "FM1"),
	}
	out := selectTopWithFMReserve(hits, 2, 0)
	if len(out) != 2 {
		t.Fatalf("len(out)=%d want 2", len(out))
	}
	for _, x := range out {
		if x.Scope == "universal/failure_modes" {
			t.Errorf("zero-reserve admitted FM at d=%.2f", x.Distance)
		}
	}
}

func TestSelectTopWithFMReserve_topNZeroIsPassthrough(t *testing.T) {
	hits := []retrievedHit{
		h(0.10, "mobile/code", "a"),
		h(0.50, "universal/failure_modes", "FM1"),
	}
	out := selectTopWithFMReserve(hits, 0, 2)
	if len(out) != 2 {
		t.Errorf("topN<=0 should passthrough, got len=%d", len(out))
	}
}
