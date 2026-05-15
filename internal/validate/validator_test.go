package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/challenge"
)

// goodOutput is a well-formed validator response — every claim is
// evidenced, every cite is plausible, evidence is substantive. Used as
// the baseline against which we mutate fields to trigger each rule.
const goodOutput = `{
  "validate": {
    "claim_summary": "Added rejection path for expired tokens with unit tests",
    "claims_parsed": [
      {"id": "c1", "text": "Added rejection path for expired tokens"},
      {"id": "c2", "text": "Added unit tests for the new path"}
    ],
    "claim_assessments": [
      {
        "claim_id": "c1",
        "verdict": "evidenced",
        "evidence": "AuthFilter.java#L42-L60 throws ExpiredTokenException on stale jwt",
        "cites": ["core-api/auth/AuthFilter.java#L42-L60"]
      },
      {
        "claim_id": "c2",
        "verdict": "evidenced",
        "evidence": "AuthFilterTest.java#L142-L168 covers the expiry path with @Test shouldRejectExpiredToken",
        "cites": ["core-api/auth/AuthFilterTest.java#L142-L168"]
      }
    ],
    "standards_satisfied": [],
    "standards_violated": [],
    "overall_verdict": "validated",
    "proceed": "merge"
  }
}`

// fakeCiteValidator builds a CiteValidator pre-seeded with the file paths
// referenced in goodOutput so Resolves() returns true for them. We rely
// on the public looksLikeFilePath gate (a "/" + matching prefix). All the
// goodOutput cites use the core-api/ prefix which is in repoPrefixes.
// Tests that need a known-term dictionary pass them via separate fixtures.
func newAVWithFiles(t *testing.T, kbRoot string) *AssessmentValidator {
	t.Helper()
	// platformRoot points at a temp dir where core-api/auth/AuthFilter.java
	// and AuthFilterTest.java exist so resolves() succeeds.
	pr := t.TempDir()
	for _, p := range []string{
		"core-api/auth/AuthFilter.java",
		"core-api/auth/AuthFilterTest.java",
	} {
		mkfile(t, pr, p)
	}
	cv, err := challenge.NewCiteValidator(pr, kbRoot)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}
	return &AssessmentValidator{Cite: cv}
}

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestValidate_GoodOutput_NoBlockers(t *testing.T) {
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(goodOutput)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if challenge.HasBlockers(vs) {
		t.Fatalf("unexpected BLOCKERs: %+v", challenge.FilterBlockers(vs))
	}
}

func TestValidate_UnparseableJSON_ReturnsBlocker(t *testing.T) {
	av := &AssessmentValidator{}
	vs, err := av.Validate(`{ not json`)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !challenge.HasBlockers(vs) {
		t.Fatalf("expected BLOCKER on unparseable JSON, got %+v", vs)
	}
	if vs[0].Code != "output_not_parseable_json" {
		t.Fatalf("expected output_not_parseable_json, got %s", vs[0].Code)
	}
}

func TestValidate_WeakEvidence_TriggersBlocker(t *testing.T) {
	mutated := strings.Replace(goodOutput,
		`"evidence": "AuthFilter.java#L42-L60 throws ExpiredTokenException on stale jwt"`,
		`"evidence": "ok"`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "weak_evidence") {
		t.Fatalf("expected weak_evidence BLOCKER, got %+v", vs)
	}
}

func TestValidate_MissingCitesForEvidenced_TriggersBlocker(t *testing.T) {
	mutated := strings.Replace(goodOutput,
		`"cites": ["core-api/auth/AuthFilter.java#L42-L60"]`,
		`"cites": []`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "missing_cites") {
		t.Fatalf("expected missing_cites BLOCKER, got %+v", vs)
	}
}

func TestValidate_UnmatchedAllowsEmptyCites(t *testing.T) {
	mutated := strings.Replace(goodOutput,
		`"verdict": "evidenced",
        "evidence": "AuthFilter.java#L42-L60 throws ExpiredTokenException on stale jwt",
        "cites": ["core-api/auth/AuthFilter.java#L42-L60"]`,
		`"verdict": "unmatched",
        "evidence": "no rejection path appears anywhere in the diff for this claim",
        "cites": []`, 1)
	// Now overall=validated but a claim is unmatched — that's a separate WARNING.
	// To isolate the cites-allowed test, also flip overall_verdict to partial.
	mutated = strings.Replace(mutated, `"overall_verdict": "validated"`, `"overall_verdict": "partial"`, 1)
	mutated = strings.Replace(mutated, `"proceed": "merge"`, `"proceed": "hold"`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if challenge.HasBlockers(vs) {
		t.Fatalf("unmatched with empty cites should be OK, got BLOCKERs: %+v", challenge.FilterBlockers(vs))
	}
}

func TestValidate_VerdictProceedMismatch_TriggersBlocker(t *testing.T) {
	// validated paired with hold — illegal coupling.
	mutated := strings.Replace(goodOutput, `"proceed": "merge"`, `"proceed": "hold"`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "verdict_proceed_mismatch") {
		t.Fatalf("expected verdict_proceed_mismatch BLOCKER, got %+v", vs)
	}
}

func TestValidate_UnknownClaimID_TriggersBlocker(t *testing.T) {
	mutated := strings.Replace(goodOutput,
		`"claim_id": "c2"`,
		`"claim_id": "zzz"`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "assessment_unknown_claim_id") {
		t.Fatalf("expected assessment_unknown_claim_id BLOCKER, got %+v", vs)
	}
}

func TestValidate_ClaimNotAssessed_TriggersBlocker(t *testing.T) {
	// Drop the second claim_assessments entry, leaving c2 unassessed.
	mutated := strings.Replace(goodOutput,
		`,
      {
        "claim_id": "c2",
        "verdict": "evidenced",
        "evidence": "AuthFilterTest.java#L142-L168 covers the expiry path with @Test shouldRejectExpiredToken",
        "cites": ["core-api/auth/AuthFilterTest.java#L142-L168"]
      }`,
		"", 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "claim_not_assessed") {
		t.Fatalf("expected claim_not_assessed BLOCKER, got %+v", vs)
	}
}

func TestValidate_EmptyClaimsParsed_TriggersBlocker(t *testing.T) {
	// Replace claims_parsed with an empty list AND claim_assessments with empty.
	mutated := strings.Replace(goodOutput,
		`"claims_parsed": [
      {"id": "c1", "text": "Added rejection path for expired tokens"},
      {"id": "c2", "text": "Added unit tests for the new path"}
    ],
    "claim_assessments": [
      {
        "claim_id": "c1",
        "verdict": "evidenced",
        "evidence": "AuthFilter.java#L42-L60 throws ExpiredTokenException on stale jwt",
        "cites": ["core-api/auth/AuthFilter.java#L42-L60"]
      },
      {
        "claim_id": "c2",
        "verdict": "evidenced",
        "evidence": "AuthFilterTest.java#L142-L168 covers the expiry path with @Test shouldRejectExpiredToken",
        "cites": ["core-api/auth/AuthFilterTest.java#L142-L168"]
      }
    ]`,
		`"claims_parsed": [],
    "claim_assessments": []`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "empty_claims_parsed") {
		t.Fatalf("expected empty_claims_parsed BLOCKER, got %+v", vs)
	}
}

func TestValidate_ValidatedWithUnmatchedClaim_TriggersWarning(t *testing.T) {
	// Keep overall_verdict=validated but flip one claim to unmatched →
	// inconsistency WARNING but NOT a BLOCKER.
	mutated := strings.Replace(goodOutput,
		`"claim_id": "c2",
        "verdict": "evidenced",
        "evidence": "AuthFilterTest.java#L142-L168 covers the expiry path with @Test shouldRejectExpiredToken",
        "cites": ["core-api/auth/AuthFilterTest.java#L142-L168"]`,
		`"claim_id": "c2",
        "verdict": "unmatched",
        "evidence": "no test file appears in the diff for this claim",
        "cites": []`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "verdict_inconsistent_with_assessment") {
		t.Fatalf("expected verdict_inconsistent_with_assessment WARNING, got %+v", vs)
	}
}

func TestValidate_CiteUnresolved_TriggersBlocker(t *testing.T) {
	mutated := strings.Replace(goodOutput,
		`"cites": ["core-api/auth/AuthFilter.java#L42-L60"]`,
		`"cites": ["definitely_not_a_real_path_or_dict_term"]`, 1)
	av := newAVWithFiles(t, t.TempDir())
	vs, err := av.Validate(mutated)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasCode(vs, "cite_unresolved") {
		t.Fatalf("expected cite_unresolved BLOCKER, got %+v", vs)
	}
}

func TestRetryPromptAddendum_NoBlockers_EmptyString(t *testing.T) {
	av := &AssessmentValidator{}
	got := av.RetryPromptAddendum(nil, nil)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRetryPromptAddendum_HasBlockers_IncludesGuidance(t *testing.T) {
	av := &AssessmentValidator{}
	vs := []challenge.Violation{{
		Severity: challenge.SeverityBlocker,
		Code:     "weak_evidence",
		Location: "claim_assessments[0].evidence",
		Value:    "ok",
		Note:     "too short",
	}}
	got := av.RetryPromptAddendum(vs, nil)
	for _, must := range []string{
		"weak_evidence",
		"CORRECTION GUIDANCE",
		"claim_assessments[].evidence",
		"unmatched is the CORRECT verdict",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("addendum missing %q, got:\n%s", must, got)
		}
	}
}

// --- helpers ---

func hasCode(vs []challenge.Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}
