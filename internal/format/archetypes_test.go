package format

import (
	"strings"
	"testing"
)

func TestArchetypes_Count(t *testing.T) {
	got := len(Archetypes())
	if got != 50 {
		t.Errorf("expected 50 archetypes, got %d", got)
	}
}

func TestArchetypes_VerdictDistribution(t *testing.T) {
	counts := map[string]int{}
	for _, a := range Archetypes() {
		counts[a.ExpectedVerdict]++
	}
	want := map[string]int{
		VerdictValid:              10,
		VerdictValidWithCaveats:   10,
		VerdictInvalid:            15,
		VerdictNeedsClarification: 10,
		VerdictOutOfScope:         5,
	}
	for v, w := range want {
		if counts[v] != w {
			t.Errorf("verdict %s: have %d want %d", v, counts[v], w)
		}
	}
}

func TestArchetypes_IDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Archetypes() {
		if a.ID == "" {
			t.Errorf("archetype has empty ID")
			continue
		}
		if seen[a.ID] {
			t.Errorf("duplicate archetype id: %s", a.ID)
		}
		seen[a.ID] = true
	}
}

func TestArchetypes_FieldsPopulated(t *testing.T) {
	for _, a := range Archetypes() {
		if strings.TrimSpace(a.Description) == "" {
			t.Errorf("%s: empty Description", a.ID)
		}
		if strings.TrimSpace(a.SeedRequest) == "" {
			t.Errorf("%s: empty SeedRequest", a.ID)
		}
		if a.ExpectedVerdict == "" {
			t.Errorf("%s: empty ExpectedVerdict", a.ID)
		}
		// INVALID + VALID + VALID_WITH_CAVEATS should have at least one
		// target cite so the gold-truth verdict carries non-empty cites
		// (HARD RULE 6). NEEDS_CLARIFICATION + OUT_OF_SCOPE may have none.
		switch a.ExpectedVerdict {
		case VerdictInvalid, VerdictValid, VerdictValidWithCaveats:
			if len(a.TargetCites) == 0 {
				t.Errorf("%s (%s): TargetCites must be non-empty for this verdict", a.ID, a.ExpectedVerdict)
			}
			for _, c := range a.TargetCites {
				if !isAcceptableCite(c) {
					t.Errorf("%s: target cite %q is not an acceptable shape", a.ID, c)
				}
			}
		}
	}
}
