package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ElatusDev/olifant/internal/challenge"
)

// AssessmentValidator inspects a validator's JSON output and reports
// violations that warrant a retry — weak evidence, missing cites,
// unresolved cite strings, claim/assessment misalignment.
//
// Wraps challenge.CiteValidator to reuse its dictionary + file-path
// resolver without duplicating the lookup logic. CiteValidator may be nil
// if the caller wants structural checks only.
type AssessmentValidator struct {
	Cite *challenge.CiteValidator
}

// validateShape is the parse target for validate-subcommand output.
type validateShape struct {
	Validate struct {
		ClaimSummary string `json:"claim_summary"`
		ClaimsParsed []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"claims_parsed"`
		ClaimAssessments []struct {
			ClaimID  string   `json:"claim_id"`
			Verdict  string   `json:"verdict"`
			Evidence string   `json:"evidence"`
			Cites    []string `json:"cites"`
		} `json:"claim_assessments"`
		StandardsSatisfied []string `json:"standards_satisfied"`
		StandardsViolated  []string `json:"standards_violated"`
		OverallVerdict     string   `json:"overall_verdict"`
		Proceed            string   `json:"proceed"`
	} `json:"validate"`
}

// verdictProceedExpected mirrors the schema's allOf coupling. Defensive
// check — the grammar layer should already prevent a mismatch, but if the
// model emits unparseable JSON the schema is bypassed.
var verdictProceedExpected = map[string]string{
	"validated": "merge",
	"partial":   "hold",
	"failed":    "block",
}

// minEvidenceChars is the floor below which an `evidence` field is
// treated as too thin to support a verdict. 20 chars is roughly "file
// path + a couple of words" — enough to point at something concrete.
const minEvidenceChars = 20

// Validate parses rawJSON and returns all violations. The contract mirrors
// challenge.CiteValidator.Validate: parse-failure produces a single
// BLOCKER violation rather than an error so the caller's retry path picks
// it up uniformly.
func (av *AssessmentValidator) Validate(rawJSON string) ([]challenge.Violation, error) {
	var s validateShape
	if err := json.Unmarshal([]byte(rawJSON), &s); err != nil {
		return []challenge.Violation{{
			Severity: challenge.SeverityBlocker,
			Code:     "output_not_parseable_json",
			Location: "(root)",
			Value:    truncateForDisplay(rawJSON, 80),
			Note:     fmt.Sprintf("synth output not valid JSON — likely truncated or repetition loop: %v", err),
		}}, nil
	}

	var out []challenge.Violation
	add := func(sev challenge.Severity, code, loc, val, note string) {
		out = append(out, challenge.Violation{
			Severity: sev, Code: code, Location: loc, Value: val, Note: note,
		})
	}

	v := &s.Validate

	// Verdict ↔ proceed coupling (already schema-enforced; defensive)
	if want, ok := verdictProceedExpected[v.OverallVerdict]; ok {
		if v.Proceed != want {
			add(challenge.SeverityBlocker, "verdict_proceed_mismatch", "proceed", v.Proceed,
				fmt.Sprintf("overall_verdict=%s requires proceed=%s, got proceed=%s",
					v.OverallVerdict, want, v.Proceed))
		}
	}

	// At least one parsed claim required
	if len(v.ClaimsParsed) == 0 {
		add(challenge.SeverityBlocker, "empty_claims_parsed", "claims_parsed", "",
			"claims_parsed[] is empty — at least one atomic claim required")
	}

	// Every parsed claim must be assessed; assessments must reference real claim IDs
	parsedIDs := make(map[string]bool, len(v.ClaimsParsed))
	for _, p := range v.ClaimsParsed {
		parsedIDs[p.ID] = true
	}
	assessedIDs := make(map[string]bool, len(v.ClaimAssessments))
	for i, a := range v.ClaimAssessments {
		if a.ClaimID != "" {
			assessedIDs[a.ClaimID] = true
			if len(parsedIDs) > 0 && !parsedIDs[a.ClaimID] {
				add(challenge.SeverityBlocker, "assessment_unknown_claim_id",
					fmt.Sprintf("claim_assessments[%d].claim_id", i), a.ClaimID,
					"claim_id does not match any id in claims_parsed[]")
			}
		}

		// Evidence depth
		evi := strings.TrimSpace(a.Evidence)
		if len(evi) < minEvidenceChars {
			add(challenge.SeverityBlocker, "weak_evidence",
				fmt.Sprintf("claim_assessments[%d].evidence", i), evi,
				"evidence must reference concrete diff content (file path + line range + what changed); too short or empty")
		}

		// evidenced + partial require cites; unmatched legitimately has none
		if (a.Verdict == "evidenced" || a.Verdict == "partial") && len(a.Cites) == 0 {
			add(challenge.SeverityBlocker, "missing_cites",
				fmt.Sprintf("claim_assessments[%d].cites", i), a.Verdict,
				"evidenced/partial verdict requires at least one citation (diff file path or standard ID)")
		}

		// Cite resolution (only when CiteValidator is wired)
		if av.Cite != nil {
			for j, cite := range a.Cites {
				if !av.Cite.Resolves(cite) {
					add(challenge.SeverityBlocker, "cite_unresolved",
						fmt.Sprintf("claim_assessments[%d].cites[%d]", i, j), cite,
						"cite does not resolve to a dictionary term or real file path")
				}
			}
		}
	}
	for _, p := range v.ClaimsParsed {
		if !assessedIDs[p.ID] {
			add(challenge.SeverityBlocker, "claim_not_assessed",
				"claim_assessments", p.ID,
				fmt.Sprintf("claim %s appears in claims_parsed[] but has no entry in claim_assessments[]", p.ID))
		}
	}

	// Standards arrays — cite resolution
	if av.Cite != nil {
		for i, c := range v.StandardsSatisfied {
			if !av.Cite.Resolves(c) {
				add(challenge.SeverityBlocker, "cite_unresolved",
					fmt.Sprintf("standards_satisfied[%d]", i), c,
					"standard ID not present in dictionary")
			}
		}
		for i, c := range v.StandardsViolated {
			if !av.Cite.Resolves(c) {
				add(challenge.SeverityBlocker, "cite_unresolved",
					fmt.Sprintf("standards_violated[%d]", i), c,
					"standard ID not present in dictionary")
			}
		}
	}

	// validated overall but a claim came back unmatched → inconsistent (WARNING)
	if v.OverallVerdict == "validated" {
		for i, a := range v.ClaimAssessments {
			if a.Verdict == "unmatched" {
				add(challenge.SeverityWarning, "verdict_inconsistent_with_assessment",
					fmt.Sprintf("claim_assessments[%d].verdict", i), a.Verdict,
					"overall_verdict=validated but at least one claim is unmatched — overall should be partial")
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		return out[i].Location < out[j].Location
	})
	return out, nil
}

// RetryPromptAddendum builds a focused correction suffix listing every
// BLOCKER violation so the model has full context for the next attempt.
// Mirrors the challenge package's pattern; calls into CiteValidator's
// per-scope term enumeration when one is supplied.
func (av *AssessmentValidator) RetryPromptAddendum(violations []challenge.Violation, scopes []string) string {
	blockers := challenge.FilterBlockers(violations)
	if len(blockers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nYOUR PREVIOUS RESPONSE HAD BLOCKER ISSUES (all must be fixed):\n")
	for _, v := range blockers {
		fmt.Fprintf(&sb, "  - [%s] %s @ %s", v.Code, v.Note, v.Location)
		if v.Value != "" {
			fmt.Fprintf(&sb, "  (value: %q)", v.Value)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nCORRECTION GUIDANCE:\n")
	sb.WriteString("1. Every claim_assessments[].evidence MUST be >= 20 chars and reference concrete diff content — a file path, line range, or specific changed identifier. Vague phrasing (\"the code adheres to standards\") is not evidence.\n")
	sb.WriteString("2. evidenced and partial verdicts MUST cite at least one source — either a real file path from the diff (e.g., core-api/.../Foo.java#L42-L60) or a standard ID from the RETRIEVED CONTEXT.\n")
	sb.WriteString("3. unmatched is the CORRECT verdict when no evidence exists. Use it. Do NOT fabricate evidence to upgrade a verdict.\n")
	sb.WriteString("4. claim_assessments[].claim_id must equal one of claims_parsed[].id verbatim. Every parsed claim MUST have an assessment.\n")
	sb.WriteString("5. standards_satisfied / standards_violated must be artifact IDs that appear in the RETRIEVED CONTEXT. Empty arrays [] are correct when no real standard ID applies.\n")
	sb.WriteString("6. If even one claim is unmatched, the overall_verdict cannot be \"validated\" — use \"partial\" (and proceed=\"hold\").\n")

	if av.Cite != nil {
		if concepts := av.Cite.TermsForScopes(challenge.LayerConcept, scopes); len(concepts) > 0 {
			fmt.Fprintf(&sb, "\nLEGAL CONCEPT NAMES (use these — never invent):\n  %s\n", strings.Join(concepts, ", "))
		}
		if constraints := av.Cite.TermsForScopes(challenge.LayerConstraint, scopes); len(constraints) > 0 {
			fmt.Fprintf(&sb, "\nLEGAL CONSTRAINT NAMES (cite these in standards_violated when the claim breaks a hard rule):\n  %s\n", strings.Join(constraints, ", "))
		}
	}
	sb.WriteString("\nFor artifact IDs (D###, AP##, SB-##, etc.) and file paths: use ONLY those that appear in the RETRIEVED CONTEXT or DIFF.\n")
	sb.WriteString("\nProduce the corrected validation now.\n")
	return sb.String()
}

func truncateForDisplay(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
