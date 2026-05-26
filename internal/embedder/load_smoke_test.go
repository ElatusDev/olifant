package embedder

import (
	"os"
	"testing"
)

// TestLoadProseRealCorpus is a smoke test against the actual v2-curriculum
// prose corpus, run only when OLIFANT_SMOKE=1.
func TestLoadProseRealCorpus(t *testing.T) {
	if os.Getenv("OLIFANT_SMOKE") != "1" {
		t.Skip("set OLIFANT_SMOKE=1 to run against real corpus")
	}
	ss, err := LoadProse("../../../knowledge-base/corpus/v2-curriculum/prose")
	if err != nil {
		t.Fatalf("LoadProse: %v", err)
	}
	t.Logf("loaded %d sentences", len(ss))
	if len(ss) < 7000 {
		t.Errorf("expected ~7716 sentences, got %d", len(ss))
	}
	scopes := map[string]int{}
	roles := map[string]int{}
	for _, s := range ss {
		scopes[s.Scope]++
		roles[s.SemanticRole]++
	}
	t.Logf("scopes: %v", scopes)
	t.Logf("roles: %v", roles)
}
