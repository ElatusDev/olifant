package challenge

import (
	"os"
	"path/filepath"
	"testing"
)

// tempPlatform creates <root>/knowledge-base/eval/failure-modes/v1.yaml and
// returns a CiteValidator rooted there.
func tempPlatform(t *testing.T) *CiteValidator {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	fm := filepath.Join(kb, "eval", "failure-modes")
	if err := os.MkdirAll(fm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fm, "v1.yaml"), []byte("FM1: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := NewCiteValidator(root, kb)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}
	return v
}

func TestNormalizeHitProvenance(t *testing.T) {
	v := tempPlatform(t)
	hits := []retrievedHit{
		{Meta: map[string]interface{}{"source_anchor": "eval/failure-modes/v1.yaml#FM1"}},
		{Meta: map[string]interface{}{"source": "knowledge-base/eval/failure-modes/v1.yaml"}},
		{Meta: map[string]interface{}{"source": "does/not/exist.md"}},
		{Meta: map[string]interface{}{}},
	}
	normalizeHitProvenance(v, hits)

	if got := hits[0].Meta["source_anchor"]; got != "knowledge-base/eval/failure-modes/v1.yaml#FM1" {
		t.Errorf("KB-relative anchor not normalized: %v", got)
	}
	if got := hits[1].Meta["source"]; got != "knowledge-base/eval/failure-modes/v1.yaml" {
		t.Errorf("already-resolvable source must be untouched: %v", got)
	}
	if got := hits[2].Meta["source"]; got != "does/not/exist.md" {
		t.Errorf("unresolvable-either-way source must be untouched: %v", got)
	}
}

func TestNormalizeHitProvenanceNilValidator(t *testing.T) {
	hits := []retrievedHit{{Meta: map[string]interface{}{"source": "eval/failure-modes/v1.yaml"}}}
	normalizeHitProvenance(nil, hits)
	if got := hits[0].Meta["source"]; got != "eval/failure-modes/v1.yaml" {
		t.Errorf("nil validator must be a no-op: %v", got)
	}
}
