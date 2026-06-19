package challenge

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSeverityString(t *testing.T) {
	if SeverityBlocker.String() != "BLOCKER" || SeverityWarning.String() != "WARNING" || SeverityInfo.String() != "INFO" {
		t.Errorf("Severity.String mismatch: %s/%s/%s",
			SeverityBlocker.String(), SeverityWarning.String(), SeverityInfo.String())
	}
}

func TestTruncateForDisplay(t *testing.T) {
	if got := truncateForDisplay("  short  ", 80); got != "short" {
		t.Errorf("trim = %q", got)
	}
	long := strings.Repeat("a", 100)
	got := truncateForDisplay(long, 10)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 11 {
		t.Errorf("truncate = %q (len %d)", got, len([]rune(got)))
	}
}

func TestHasBlockersAndFilterBlockers(t *testing.T) {
	vs := []Violation{
		{Severity: SeverityBlocker, Code: "a"},
		{Severity: SeverityWarning, Code: "b"},
		{Severity: SeverityBlocker, Code: "c"},
	}
	if !HasBlockers(vs) {
		t.Error("should report blockers present")
	}
	if HasBlockers([]Violation{{Severity: SeverityWarning}}) {
		t.Error("warning-only should not report blocker")
	}
	if got := FilterBlockers(vs); len(got) != 2 {
		t.Errorf("FilterBlockers = %d, want 2", len(got))
	}
}

func TestEnumArrayOrEmpty(t *testing.T) {
	empty := enumArrayOrEmpty(nil)
	if empty["maxItems"].(int) != 0 {
		t.Errorf("empty values should cap at 0: %v", empty)
	}
	full := enumArrayOrEmpty([]string{"D1", "AP2"})
	items, ok := full["items"].(map[string]interface{})
	if !ok || items["enum"] == nil {
		t.Errorf("non-empty should carry enum: %v", full)
	}
}

func TestFilterByPattern(t *testing.T) {
	terms := []string{"SB-04", "lowercase", "D154", "AP78"}
	re := regexp.MustCompile(`^[A-Z]`)
	got := filterByPattern(terms, re)
	if len(got) != 3 {
		t.Errorf("filterByPattern = %v, want 3 upper-case-leading", got)
	}
	if filterByPattern(nil, re) != nil {
		t.Error("empty input should return nil")
	}
}

func TestResultExtractVerdict(t *testing.T) {
	r := &Result{RawJSON: `{"challenge":{"verdict":"VALID","proceed":"proceed_directly"}}`}
	v, p := r.ExtractVerdict()
	if v != "VALID" || p != "proceed_directly" {
		t.Errorf("ExtractVerdict = (%q,%q)", v, p)
	}
	bad := &Result{RawJSON: "nope"}
	if v, p := bad.ExtractVerdict(); v != "" || p != "" {
		t.Errorf("invalid json = (%q,%q), want empty", v, p)
	}
}

// buildKBValidator builds a CiteValidator from a temp KB dictionary so the
// cite-resolution + schema paths can run without the real platform tree.
func buildKBValidator(t *testing.T) *CiteValidator {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	dict := filepath.Join(kb, "dictionary", "backend", "domain.yaml")
	if err := os.MkdirAll(filepath.Dir(dict), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dict, []byte("- term: SB-04\n- term: D154\n- term: AP3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := NewCiteValidator(root, kb)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}
	return v
}

func TestValidatorCounts(t *testing.T) {
	v := buildKBValidator(t)
	if v.KnownCount() != 3 {
		t.Errorf("KnownCount = %d, want 3", v.KnownCount())
	}
	if v.CountByLayer(LayerDictionary) != 3 {
		t.Errorf("CountByLayer(dictionary) = %d, want 3", v.CountByLayer(LayerDictionary))
	}
}

func TestValidate_ParseError(t *testing.T) {
	v := buildKBValidator(t)
	vs, err := v.Validate("definitely not json")
	if err != nil {
		t.Fatalf("Validate returns violation not error: %v", err)
	}
	if !HasBlockers(vs) || vs[0].Code != "output_not_parseable_json" {
		t.Errorf("parse error should yield parse blocker, got %v", vs)
	}
}

func TestValidate_CleanValid(t *testing.T) {
	v := buildKBValidator(t)
	// VALID with substantive request, resolvable cites, correct proceed.
	clean := `{"challenge":{
		"request":"add a tenant scoped invoice entity",
		"verdict":"VALID",
		"proceed":"proceed_directly",
		"confirms":[{"claim":"ok","cites":["SB-04"]}],
		"applicable_rules":{"standards":["D154"],"patterns":[],"anti_patterns_to_avoid":["AP3"],"decisions_to_honor":[]}
	}}`
	vs, _ := v.Validate(clean)
	if HasBlockers(vs) {
		t.Errorf("clean VALID output should have no blockers, got %v", vs)
	}
}

func TestValidate_UnresolvedCiteAndMismatch(t *testing.T) {
	v := buildKBValidator(t)
	bad := `{"challenge":{
		"request":"add a tenant scoped invoice entity",
		"verdict":"VALID",
		"proceed":"abort",
		"confirms":[{"claim":"ok","cites":["D999"]}],
		"applicable_rules":{"standards":[],"patterns":[],"anti_patterns_to_avoid":[],"decisions_to_honor":[]}
	}}`
	vs, _ := v.Validate(bad)
	codes := map[string]bool{}
	for _, x := range vs {
		codes[x.Code] = true
	}
	if !codes["cite_unresolved"] {
		t.Errorf("expected cite_unresolved for D999, got %v", vs)
	}
	if !codes["verdict_proceed_mismatch"] {
		t.Errorf("expected verdict_proceed_mismatch (VALID wants proceed_directly), got %v", vs)
	}
}

func TestValidate_PlaceholderRequestAndInvalidWithoutContradicts(t *testing.T) {
	v := buildKBValidator(t)
	bad := `{"challenge":{
		"request":"none",
		"verdict":"INVALID",
		"proceed":"abort",
		"contradicts":[]
	}}`
	vs, _ := v.Validate(bad)
	codes := map[string]bool{}
	for _, x := range vs {
		codes[x.Code] = true
	}
	if !codes["placeholder_request"] {
		t.Errorf("expected placeholder_request, got %v", vs)
	}
	if !codes["invalid_without_contradicts"] {
		t.Errorf("expected invalid_without_contradicts, got %v", vs)
	}
}

func TestRetryPromptAddendum(t *testing.T) {
	v := buildKBValidator(t)
	// No blockers → empty addendum.
	if got := v.RetryPromptAddendum([]Violation{{Severity: SeverityWarning}}, "req", nil); got != "" {
		t.Errorf("no-blocker addendum should be empty, got %q", got)
	}
	// With a blocker → guidance text referencing the code.
	got := v.RetryPromptAddendum([]Violation{
		{Severity: SeverityBlocker, Code: "cite_unresolved", Location: "confirms[0].cites[0]", Value: "D999", Note: "n"},
	}, "req", []string{"backend"})
	if !strings.Contains(got, "BLOCKER ISSUES") || !strings.Contains(got, "cite_unresolved") {
		t.Errorf("addendum missing guidance:\n%s", got)
	}
}

func TestCitesSchema_WithValidator(t *testing.T) {
	v := buildKBValidator(t)
	schema := v.CitesSchema([]string{"backend"}, 8)
	if schema == nil {
		t.Fatal("CitesSchema returned nil")
	}
	if schema["type"] != "array" {
		t.Errorf("cites schema type = %v, want array", schema["type"])
	}
}
