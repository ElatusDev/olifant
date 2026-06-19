package challenge

import (
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/retrieval"
)

func TestBuildChallengeSchema(t *testing.T) {
	// Nil validator → base schema, still well-formed.
	base := BuildChallengeSchema(nil, nil)
	if base == nil || base["type"] != "object" {
		t.Fatalf("nil-validator schema = %v", base)
	}

	// With validator → dynamic enums from dictionary terms.
	v := buildKBValidator(t)
	dyn := BuildChallengeSchema(v, []string{"backend"})
	if dyn == nil {
		t.Fatal("validator schema nil")
	}
	props, ok := dyn["properties"].(map[string]interface{})
	if !ok || props["challenge"] == nil {
		t.Errorf("dynamic schema missing challenge property: %v", dyn)
	}
}

func TestBuildChallengePrompt(t *testing.T) {
	hits := []retrievedHit{
		{Doc: "chunk body one", Scope: "backend", Distance: 0.1, Meta: retrieval.Hit{}.Meta},
	}
	hits[0].Meta = map[string]interface{}{"source": "patterns/backend.md", "artifact_id": "AP3"}
	out := buildChallengePrompt("review this code", hits)
	for _, want := range []string{"USER REQUEST", "review this code", "artifact_id=AP3", "chunk body one"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}

	// Empty hits still renders the request block.
	if got := buildChallengePrompt("x", nil); !strings.Contains(got, "USER REQUEST") {
		t.Errorf("empty-hits prompt missing request block:\n%s", got)
	}
}

func TestValidate_ClarificationAndScopeWarnings(t *testing.T) {
	v := buildKBValidator(t)

	// NEEDS_CLARIFICATION with no clarify[] → blocker.
	needsClar := `{"challenge":{"request":"add invoices entity to backend","verdict":"NEEDS_CLARIFICATION","proceed":"confirm_with_user","clarify":[]}}`
	vs, _ := v.Validate(needsClar)
	if !hasCode(vs, "clarify_required_but_empty") {
		t.Errorf("expected clarify_required_but_empty, got %v", vs)
	}

	// OUT_OF_SCOPE that also confirms → warning.
	oos := `{"challenge":{"request":"build a spaceship navigation system","verdict":"OUT_OF_SCOPE","proceed":"abort","confirms":[{"claim":"x","cites":["SB-04"]}]}}`
	vs2, _ := v.Validate(oos)
	if !hasCode(vs2, "out_of_scope_with_confirms") {
		t.Errorf("expected out_of_scope_with_confirms warning, got %v", vs2)
	}

	// request too short (no space, <10 chars) → blocker.
	short := `{"challenge":{"request":"hi","verdict":"VALID","proceed":"proceed_directly"}}`
	vs3, _ := v.Validate(short)
	if !hasCode(vs3, "request_too_short") {
		t.Errorf("expected request_too_short, got %v", vs3)
	}

	// confirms[] entry with no cites → blocker.
	noCites := `{"challenge":{"request":"add a tenant scoped invoice entity","verdict":"VALID","proceed":"proceed_directly","confirms":[{"claim":"x","cites":[]}]}}`
	vs4, _ := v.Validate(noCites)
	if !hasCode(vs4, "confirms_unsupported") {
		t.Errorf("expected confirms_unsupported, got %v", vs4)
	}
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}
