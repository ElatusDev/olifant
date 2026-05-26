// Package format provides the Phase C1 verdict-YAML training-pair pipeline.
//
// The package defines a TIGHTER schema than challenge.planSynthSchema —
// the production challenge schema can't carry minItems / conditional
// rules without crashing Ollama's grammar engine, but a Format-LoRA
// trained on tight pairs will learn the verdict-content coherence rules
// implicitly so the production schema doesn't need to enforce them.
//
// Phase reference: olifant-rag-pivot-workflow.md §4 Phase C, §6 AC #3.
package format

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Verdict enum — matches challenge.systemPrompt's allowed values.
const (
	VerdictValid              = "VALID"
	VerdictValidWithCaveats   = "VALID_WITH_CAVEATS"
	VerdictInvalid            = "INVALID"
	VerdictNeedsClarification = "NEEDS_CLARIFICATION"
	VerdictOutOfScope         = "OUT_OF_SCOPE"
)

// Proceed enum — derived from verdict per the documented mapping in
// challenge.systemPrompt:
//
//	VALID                → proceed_directly
//	VALID_WITH_CAVEATS   → confirm_with_user
//	INVALID              → abort
//	NEEDS_CLARIFICATION  → confirm_with_user
//	OUT_OF_SCOPE         → abort
const (
	ProceedDirectly        = "proceed_directly"
	ProceedConfirmWithUser = "confirm_with_user"
	ProceedAbort           = "abort"
)

// VerdictDoc is the parsed form of one verdict-YAML training response.
// The YAML on disk has a single top-level "challenge:" key (per
// challenge.systemPrompt examples 1-4); we model the inner shape.
type VerdictDoc struct {
	Challenge ChallengeBody `yaml:"challenge"`
}

// ChallengeBody is the inner challenge: object the model produces.
type ChallengeBody struct {
	Request         string          `yaml:"request"`
	Verdict         string          `yaml:"verdict"`
	Confirms        []Confirm       `yaml:"confirms"`
	Contradicts     []Contradict    `yaml:"contradicts"`
	Clarify         []Clarification `yaml:"clarify"`
	ApplicableRules ApplicableRules `yaml:"applicable_rules"`
	Proceed         string          `yaml:"proceed"`
}

// Confirm is one confirms[] entry — a positive alignment claim.
type Confirm struct {
	Claim string   `yaml:"claim"`
	Cites []string `yaml:"cites"`
}

// Contradict is one contradicts[] entry — a negative violation claim
// with required counter-explanation.
type Contradict struct {
	Claim   string   `yaml:"claim"`
	Counter string   `yaml:"counter"`
	Cites   []string `yaml:"cites"`
}

// Clarification is one clarify[] entry — a question for the user with
// the reason it's being asked.
type Clarification struct {
	Question  string `yaml:"question"`
	WhyAsking string `yaml:"why_asking"`
}

// ApplicableRules is the four-bucket pointers structure.
type ApplicableRules struct {
	Standards           []string `yaml:"standards"`
	Patterns            []string `yaml:"patterns"`
	AntiPatternsToAvoid []string `yaml:"anti_patterns_to_avoid"`
	DecisionsToHonor    []string `yaml:"decisions_to_honor"`
}

// ParseVerdictYAML parses raw YAML bytes into a VerdictDoc. It does
// NOT validate semantic rules — call Validate for that.
func ParseVerdictYAML(raw []byte) (*VerdictDoc, error) {
	var doc VerdictDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return &doc, nil
}

// Validate enforces the verdict-content coherence rules that the
// production grammar engine can't carry. Each rule mirrors a challenge
// systemPrompt HARD RULE (see internal/challenge/runner.go).
//
// Returns nil iff the doc is valid; otherwise a multi-violation error
// where each line names one specific rule violation.
func (d *VerdictDoc) Validate() error {
	if d == nil {
		return fmt.Errorf("nil verdict doc")
	}
	c := d.Challenge
	var v []string

	// HARD RULE: request non-empty.
	if strings.TrimSpace(c.Request) == "" {
		v = append(v, "challenge.request: must be non-empty")
	}

	// Verdict enum constraint.
	switch c.Verdict {
	case VerdictValid, VerdictValidWithCaveats, VerdictInvalid,
		VerdictNeedsClarification, VerdictOutOfScope:
		// ok
	default:
		v = append(v, fmt.Sprintf("challenge.verdict: %q not in allowed enum", c.Verdict))
	}

	// HARD RULE: proceed derived from verdict.
	wantProceed := proceedForVerdict(c.Verdict)
	if wantProceed != "" && c.Proceed != wantProceed {
		v = append(v, fmt.Sprintf("challenge.proceed: %q does not match verdict %q (expected %q)",
			c.Proceed, c.Verdict, wantProceed))
	}

	// HARD RULE 5: verdict-content coherence.
	switch c.Verdict {
	case VerdictNeedsClarification:
		if len(c.Clarify) == 0 {
			v = append(v, "NEEDS_CLARIFICATION requires ≥1 clarify[] entry (HARD RULE 5)")
		}
	case VerdictInvalid:
		if len(c.Contradicts) == 0 {
			v = append(v, "INVALID requires ≥1 contradicts[] entry (HARD RULE 5)")
		}
	case VerdictValid:
		if len(c.Contradicts) > 0 {
			v = append(v, "VALID forbids contradicts[] entries (HARD RULE 5)")
		}
	case VerdictValidWithCaveats:
		if len(c.Contradicts) > 0 {
			v = append(v, "VALID_WITH_CAVEATS forbids contradicts[] entries (HARD RULE 5)")
		}
		if len(c.Confirms) == 0 && len(c.Clarify) == 0 {
			v = append(v, "VALID_WITH_CAVEATS requires ≥1 entry in confirms[] OR clarify[] (HARD RULE 5)")
		}
	}

	// HARD RULE 6: cite non-emptiness.
	for i, ce := range c.Confirms {
		if strings.TrimSpace(ce.Claim) == "" {
			v = append(v, fmt.Sprintf("confirms[%d].claim: must be non-empty", i))
		}
		if len(ce.Cites) == 0 {
			v = append(v, fmt.Sprintf("confirms[%d]: requires ≥1 cite (HARD RULE 6)", i))
		}
		for j, cite := range ce.Cites {
			if !isAcceptableCite(cite) {
				v = append(v, fmt.Sprintf("confirms[%d].cites[%d]: %q is not an acceptable cite shape (HARD RULE 1)", i, j, cite))
			}
		}
	}
	for i, ct := range c.Contradicts {
		if strings.TrimSpace(ct.Claim) == "" {
			v = append(v, fmt.Sprintf("contradicts[%d].claim: must be non-empty", i))
		}
		if strings.TrimSpace(ct.Counter) == "" {
			v = append(v, fmt.Sprintf("contradicts[%d].counter: must be non-empty", i))
		}
		if len(ct.Cites) == 0 {
			v = append(v, fmt.Sprintf("contradicts[%d]: requires ≥1 cite (HARD RULE 6)", i))
		}
		for j, cite := range ct.Cites {
			if !isAcceptableCite(cite) {
				v = append(v, fmt.Sprintf("contradicts[%d].cites[%d]: %q is not an acceptable cite shape (HARD RULE 1)", i, j, cite))
			}
		}
	}
	for i, cl := range c.Clarify {
		if strings.TrimSpace(cl.Question) == "" {
			v = append(v, fmt.Sprintf("clarify[%d].question: must be non-empty", i))
		}
		if strings.TrimSpace(cl.WhyAsking) == "" {
			v = append(v, fmt.Sprintf("clarify[%d].why_asking: must be non-empty", i))
		}
	}

	if len(v) == 0 {
		return nil
	}
	return fmt.Errorf("verdict validation failed:\n  - %s", strings.Join(v, "\n  - "))
}

// proceedForVerdict returns the canonical proceed value for a verdict.
// Empty string means "verdict not in enum, skip the check".
func proceedForVerdict(verdict string) string {
	switch verdict {
	case VerdictValid:
		return ProceedDirectly
	case VerdictValidWithCaveats, VerdictNeedsClarification:
		return ProceedConfirmWithUser
	case VerdictInvalid, VerdictOutOfScope:
		return ProceedAbort
	}
	return ""
}

// isAcceptableCite enforces HARD RULE 1's cite-shape rules in a single
// helper — used by Validate and reusable by ad-hoc callers.
//
// Acceptable shapes:
//   - artifact ID prefix: D###, AP##, PC##, FM##, SB-##, SI-##, IV##,
//     IMF##, AMS-##, AWS-##, ABS-##, WA-..., AM<letter>-##, AW<letter>-##,
//     AB<letter>-##, TBU-##
//   - fully-qualified path: contains a "/" AND starts with a known repo
//     prefix OR knowledge-base/
//
// Rejected:
//   - bare filenames (README.md, CLAUDE.md, *.md without "/")
//   - partial paths (.claude/prompts/...)
//   - chunk display labels (chunk1, chunk2, ...)
//   - generic categories (magic_strings, owasp_top10, ...)
func isAcceptableCite(cite string) bool {
	s := strings.TrimSpace(cite)
	if s == "" {
		return false
	}
	if isArtifactID(s) {
		return true
	}
	if isFullyQualifiedPath(s) {
		return true
	}
	return false
}

// repoPrefixes — the seven platform repos + the knowledge-base root.
// Used by isFullyQualifiedPath.
var repoPrefixes = []string{
	"core-api/",
	"akademia-plus-web/",
	"elatusdev-web/",
	"akademia-plus-central/",
	"akademia-plus-go/",
	"core-api-e2e/",
	"infra/",
	"knowledge-base/",
}

// artifactIDPrefixes — recognised artifact ID families. Matching is
// prefix-based; we don't validate the trailing digits/letters.
var artifactIDPrefixes = []string{
	"D", "AP", "PC", "FM", "SB-", "SI-", "IV", "IMF",
	"AMS-", "AWS-", "ABS-", "WA-",
	"AMP-", "AMC-", "AMS-", "AMN-", "AMH-", "AME-", "AMTA-",
	"AWC-", "AWH-", "AWS-", "AWR-", "AWT-", "AWB-", "AWTA-", "AWA-",
	"ABB-", "ABO-", "ABC-", "ABD-", "ABE-", "ABS-", "ABT-",
	"TBU-",
}

func isArtifactID(s string) bool {
	// Each prefix must be followed by EITHER a digit (D17, AP3, PC15,
	// AMS-02) OR a single capital letter then a digit (WA-W03 — webapp
	// architecture subcategory codes). The trailing-digit requirement
	// prevents matching words like "DATA" or "APPLICATION".
	for _, p := range artifactIDPrefixes {
		if !strings.HasPrefix(s, p) {
			continue
		}
		rest := s[len(p):]
		if rest == "" {
			continue
		}
		c := rest[0]
		if c >= '0' && c <= '9' {
			return true
		}
		if c >= 'A' && c <= 'Z' && len(rest) >= 2 {
			next := rest[1]
			if next >= '0' && next <= '9' {
				return true
			}
		}
	}
	return false
}

func isFullyQualifiedPath(s string) bool {
	if !strings.Contains(s, "/") {
		return false
	}
	for _, p := range repoPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
