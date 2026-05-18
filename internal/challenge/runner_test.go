package challenge

import (
	"testing"
)

// h builds a minimal hit fixture for the selectTopWithFMReserve tests.
// Distance is the only ranking signal needed; Scope drives FM vs non-FM
// classification.
func h(distance float32, scope, label string) retrievedHit {
	return retrievedHit{Distance: distance, Scope: scope, Meta: map[string]interface{}{"source": label}}
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
