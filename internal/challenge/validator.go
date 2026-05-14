package challenge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Severity classifies how much a violation matters.
//
// BLOCKER  — the output is wrong; retry is justified
// WARNING  — the output is suboptimal but usable; surface to user
// INFO     — pattern worth noting; no action
type Severity int

const (
	SeverityBlocker Severity = iota
	SeverityWarning
	SeverityInfo
)

func (s Severity) String() string {
	switch s {
	case SeverityBlocker:
		return "BLOCKER"
	case SeverityWarning:
		return "WARNING"
	default:
		return "INFO"
	}
}

// Violation is one rule-break found in a challenge output.
type Violation struct {
	Severity Severity
	Code     string // stable identifier, e.g., "cite_unresolved"
	Location string // dotted path, e.g., "confirms[0].cites[1]"
	Value    string // the offending value (may be empty)
	Note     string // human-readable explanation
}

// CiteValidator holds the corpus of legal terms + file paths and exposes
// extensive post-output validation. Reusable across many calls.
type CiteValidator struct {
	dictTerms    map[string]struct{}
	platformRoot string
	repoPrefixes []string
}

// NewCiteValidator loads dictionary entries once.
func NewCiteValidator(platformRoot, dictDir string) (*CiteValidator, error) {
	v := &CiteValidator{
		dictTerms:    map[string]struct{}{},
		platformRoot: platformRoot,
		repoPrefixes: []string{
			"core-api", "akademia-plus-web", "elatusdev-web",
			"akademia-plus-central", "akademia-plus-go",
			"core-api-e2e", "infra",
			"knowledge-base",
			"decisions", "anti-patterns", "standards", "patterns",
			"workflows", "prompts", "retrospectives", "templates",
			"skills", "operations", "architecture", "audit-report",
			"ux", "views",
		},
	}
	if err := filepath.Walk(dictDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var entries []struct {
			Term     string   `yaml:"term"`
			Synonyms []string `yaml:"synonyms"`
		}
		if uerr := yaml.Unmarshal(raw, &entries); uerr != nil {
			return nil
		}
		for _, e := range entries {
			if e.Term != "" {
				v.dictTerms[e.Term] = struct{}{}
			}
			for _, s := range e.Synonyms {
				v.dictTerms[s] = struct{}{}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return v, nil
}

// KnownCount returns the number of dictionary terms loaded (informational).
func (v *CiteValidator) KnownCount() int { return len(v.dictTerms) }

// challengeShape is the minimal parse target for validation.
type challengeShape struct {
	Challenge struct {
		Request     string `json:"request"`
		Verdict     string `json:"verdict"`
		Proceed     string `json:"proceed"`
		Confirms    []struct {
			Claim string   `json:"claim"`
			Cites []string `json:"cites"`
		} `json:"confirms"`
		Contradicts []struct {
			Claim   string   `json:"claim"`
			Counter string   `json:"counter"`
			Cites   []string `json:"cites"`
		} `json:"contradicts"`
		Clarify []struct {
			Question  string `json:"question"`
			WhyAsking string `json:"why_asking"`
		} `json:"clarify"`
		ApplicableRules struct {
			Standards           []string `json:"standards"`
			Patterns            []string `json:"patterns"`
			AntiPatternsToAvoid []string `json:"anti_patterns_to_avoid"`
			DecisionsToHonor    []string `json:"decisions_to_honor"`
		} `json:"applicable_rules"`
	} `json:"challenge"`
}

// verdictProceedExpected maps each verdict to its required proceed value.
var verdictProceedExpected = map[string]string{
	"VALID":               "proceed_directly",
	"VALID_WITH_CAVEATS":  "confirm_with_user",
	"INVALID":             "abort",
	"NEEDS_CLARIFICATION": "confirm_with_user",
	"OUT_OF_SCOPE":        "abort",
}

// placeholderRequests are non-substantive values the model sometimes drops in.
var placeholderRequests = map[string]struct{}{
	"clarification":         {},
	"clarification_required": {},
	"clarification_not_required": {},
	"no_changes_required":   {},
	"request_clarification": {},
	"none_required":         {},
	"none":                  {},
	"n/a":                   {},
	"na":                    {},
}

// Validate parses the synth output and returns ALL violations across the rule
// set. Callers decide which to retry on (typically BLOCKER) and which to
// surface (WARNING) or log (INFO).
func (v *CiteValidator) Validate(rawJSON string) ([]Violation, error) {
	var c challengeShape
	if err := json.Unmarshal([]byte(rawJSON), &c); err != nil {
		return nil, fmt.Errorf("validator: parse JSON: %w", err)
	}

	var out []Violation
	add := func(sev Severity, code, loc, val, note string) {
		out = append(out, Violation{Severity: sev, Code: code, Location: loc, Value: val, Note: note})
	}

	// === Group 1: cite resolution (BLOCKER) ===========================
	checkCite := func(loc, c string) {
		if v.resolves(c) {
			return
		}
		add(SeverityBlocker, "cite_unresolved", loc, c,
			"value does not exist in dictionary or filesystem")
	}
	for i, e := range c.Challenge.Confirms {
		for j, cite := range e.Cites {
			checkCite(fmt.Sprintf("confirms[%d].cites[%d]", i, j), cite)
		}
	}
	for i, e := range c.Challenge.Contradicts {
		for j, cite := range e.Cites {
			checkCite(fmt.Sprintf("contradicts[%d].cites[%d]", i, j), cite)
		}
	}
	for i, cite := range c.Challenge.ApplicableRules.Standards {
		checkCite(fmt.Sprintf("applicable_rules.standards[%d]", i), cite)
	}
	for i, cite := range c.Challenge.ApplicableRules.Patterns {
		checkCite(fmt.Sprintf("applicable_rules.patterns[%d]", i), cite)
	}
	for i, cite := range c.Challenge.ApplicableRules.AntiPatternsToAvoid {
		checkCite(fmt.Sprintf("applicable_rules.anti_patterns_to_avoid[%d]", i), cite)
	}
	for i, cite := range c.Challenge.ApplicableRules.DecisionsToHonor {
		checkCite(fmt.Sprintf("applicable_rules.decisions_to_honor[%d]", i), cite)
	}

	// === Group 2: verdict ↔ proceed coupling (BLOCKER) =================
	if want, ok := verdictProceedExpected[c.Challenge.Verdict]; ok {
		if c.Challenge.Proceed != want {
			add(SeverityBlocker, "verdict_proceed_mismatch", "proceed", c.Challenge.Proceed,
				fmt.Sprintf("verdict=%s requires proceed=%s, got proceed=%s",
					c.Challenge.Verdict, want, c.Challenge.Proceed))
		}
	}

	// === Group 3: structural completeness (BLOCKER) ====================
	switch c.Challenge.Verdict {
	case "INVALID":
		if len(c.Challenge.Contradicts) == 0 {
			add(SeverityBlocker, "invalid_without_contradicts", "contradicts", "",
				"verdict=INVALID requires at least one contradicts[] entry with concrete evidence")
		}
	case "NEEDS_CLARIFICATION":
		if len(c.Challenge.Clarify) == 0 {
			add(SeverityBlocker, "clarify_required_but_empty", "clarify", "",
				"verdict=NEEDS_CLARIFICATION requires at least one clarify[] question")
		}
	}

	// Each confirm/contradict entry must have cites
	for i, e := range c.Challenge.Confirms {
		if len(e.Cites) == 0 {
			add(SeverityBlocker, "confirms_unsupported",
				fmt.Sprintf("confirms[%d].cites", i), "",
				"each confirms[] entry needs at least one citation")
		}
	}
	for i, e := range c.Challenge.Contradicts {
		if len(e.Cites) == 0 {
			add(SeverityBlocker, "contradicts_unsupported",
				fmt.Sprintf("contradicts[%d].cites", i), "",
				"each contradicts[] entry needs at least one citation")
		}
	}

	// === Group 4: request field substance (BLOCKER) ====================
	req := strings.TrimSpace(strings.ToLower(c.Challenge.Request))
	if _, isPlaceholder := placeholderRequests[req]; isPlaceholder || req == "" {
		add(SeverityBlocker, "placeholder_request", "request", c.Challenge.Request,
			"request field must contain the user's verbatim request (or faithful summary), not a placeholder")
	} else if len(req) < 10 || !strings.ContainsRune(req, ' ') {
		add(SeverityBlocker, "request_too_short", "request", c.Challenge.Request,
			"request field looks like a placeholder; must be a real sentence")
	}

	// === Group 5: OUT_OF_SCOPE consistency (WARNING) ===================
	if c.Challenge.Verdict == "OUT_OF_SCOPE" {
		if len(c.Challenge.Confirms) > 0 {
			add(SeverityWarning, "out_of_scope_with_confirms", "confirms", "",
				"OUT_OF_SCOPE should not also confirm findings — corpus doesn't cover the topic")
		}
		if len(c.Challenge.Contradicts) > 0 {
			add(SeverityWarning, "out_of_scope_with_contradicts", "contradicts", "",
				"OUT_OF_SCOPE should not also contradict — corpus doesn't cover the topic")
		}
	}

	// === Group 6: empty assessment (WARNING) ===========================
	// VALID with everything empty is the CORRECT answer for clean code, so
	// only flag empty assessment for the non-VALID/non-OUT_OF_SCOPE verdicts.
	switch c.Challenge.Verdict {
	case "VALID_WITH_CAVEATS", "INVALID", "NEEDS_CLARIFICATION":
		if len(c.Challenge.Confirms) == 0 &&
			len(c.Challenge.Contradicts) == 0 &&
			len(c.Challenge.Clarify) == 0 {
			add(SeverityWarning, "empty_assessment", "challenge", "",
				fmt.Sprintf("verdict=%s but confirms/contradicts/clarify are all empty — assessment lacks substance",
					c.Challenge.Verdict))
		}
	}

	// === Group 7: duplicates within a single list (INFO) ===============
	checkDupes := func(loc string, items []string) {
		seen := map[string]int{}
		for i, it := range items {
			seen[it]++
			if seen[it] == 2 {
				add(SeverityInfo, "duplicate_cite",
					fmt.Sprintf("%s[%d]", loc, i), it,
					"value already appears earlier in the same list")
			}
		}
	}
	checkDupes("applicable_rules.standards", c.Challenge.ApplicableRules.Standards)
	checkDupes("applicable_rules.patterns", c.Challenge.ApplicableRules.Patterns)
	checkDupes("applicable_rules.anti_patterns_to_avoid", c.Challenge.ApplicableRules.AntiPatternsToAvoid)
	checkDupes("applicable_rules.decisions_to_honor", c.Challenge.ApplicableRules.DecisionsToHonor)
	for i, e := range c.Challenge.Confirms {
		checkDupes(fmt.Sprintf("confirms[%d].cites", i), e.Cites)
	}
	for i, e := range c.Challenge.Contradicts {
		checkDupes(fmt.Sprintf("contradicts[%d].cites", i), e.Cites)
	}

	// === Group 8: short claims (INFO) ==================================
	for i, e := range c.Challenge.Confirms {
		if len(strings.TrimSpace(e.Claim)) > 0 && len(strings.TrimSpace(e.Claim)) < 20 {
			add(SeverityInfo, "short_claim",
				fmt.Sprintf("confirms[%d].claim", i), e.Claim,
				"claim is unusually short; likely lacks substance")
		}
	}
	for i, e := range c.Challenge.Contradicts {
		if len(strings.TrimSpace(e.Claim)) > 0 && len(strings.TrimSpace(e.Claim)) < 20 {
			add(SeverityInfo, "short_claim",
				fmt.Sprintf("contradicts[%d].claim", i), e.Claim,
				"claim is unusually short; likely lacks substance")
		}
	}

	// Sort for stable output: by Severity, then Location.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		return out[i].Location < out[j].Location
	})
	return out, nil
}

// HasBlockers returns true if any BLOCKER-severity violation is present.
func HasBlockers(vs []Violation) bool {
	for _, v := range vs {
		if v.Severity == SeverityBlocker {
			return true
		}
	}
	return false
}

// FilterBlockers returns just the BLOCKER subset.
func FilterBlockers(vs []Violation) []Violation {
	out := make([]Violation, 0, len(vs))
	for _, v := range vs {
		if v.Severity == SeverityBlocker {
			out = append(out, v)
		}
	}
	return out
}

// resolves returns true if `c` is a known artifact ID OR resolves to a real
// platform file path (with optional #anchor or #Lstart-Lend suffix).
func (v *CiteValidator) resolves(c string) bool {
	c = strings.TrimSpace(c)
	if c == "" {
		return false
	}
	base := c
	if hashIdx := strings.IndexByte(c, '#'); hashIdx >= 0 {
		base = c[:hashIdx]
	}
	if _, ok := v.dictTerms[base]; ok {
		return true
	}
	if _, ok := v.dictTerms[c]; ok {
		return true
	}
	if v.looksLikeFilePath(base) {
		return v.fileExists(base)
	}
	return false
}

var pathLikeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_./-]*\.[a-zA-Z0-9_]+$`)

func (v *CiteValidator) looksLikeFilePath(s string) bool {
	if !strings.ContainsRune(s, '/') {
		return false
	}
	if !pathLikeRE.MatchString(s) {
		return false
	}
	for _, p := range v.repoPrefixes {
		if strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

func (v *CiteValidator) fileExists(relPath string) bool {
	// Try the platform root first; then knowledge-base/ for raw KB-relative paths.
	candidates := []string{
		filepath.Join(v.platformRoot, relPath),
		filepath.Join(v.platformRoot, "knowledge-base", relPath),
	}
	for _, abs := range candidates {
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// RetryPromptAddendum builds a focused "fix your last output" suffix listing
// every BLOCKER violation so the model has full context for the next attempt.
// `originalRequest` is passed so the model can copy it verbatim if the request
// field needs correction.
func RetryPromptAddendum(violations []Violation, originalRequest string) string {
	blockers := FilterBlockers(violations)
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
	sb.WriteString("1. EMPTY ARRAYS ARE CORRECT when you can't find specific evidence in the RETRIEVED CONTEXT. ")
	sb.WriteString("Empty [] is the right answer for fields where no real artifact ID applies. ")
	sb.WriteString("Do NOT fill arrays with invented generic categories like \"magic_strings\", \"hardcoded_secrets\", ")
	sb.WriteString("\"naming_conventions\", \"dependency_injection\". Those are not citations.\n")
	sb.WriteString("2. Honor the verdict-proceed coupling: ")
	sb.WriteString("VALID→proceed_directly, VALID_WITH_CAVEATS→confirm_with_user, INVALID→abort, ")
	sb.WriteString("NEEDS_CLARIFICATION→confirm_with_user, OUT_OF_SCOPE→abort.\n")
	sb.WriteString("3. INVALID requires at least one contradicts[] entry whose cites[] resolve to real artifact IDs ")
	sb.WriteString("(D###, AP##, SB-##, …) or real source paths in the RETRIEVED CONTEXT. ")
	sb.WriteString("If you cannot identify such evidence, do NOT use INVALID — use VALID, VALID_WITH_CAVEATS, ")
	sb.WriteString("or NEEDS_CLARIFICATION instead.\n")
	if originalRequest != "" {
		// One-line summary if multi-line (avoid the model copying the framing).
		summary := originalRequest
		if nl := strings.IndexByte(summary, '\n'); nl > 0 {
			summary = summary[:nl]
		}
		if len(summary) > 240 {
			summary = summary[:240] + "…"
		}
		fmt.Fprintf(&sb, "4. The 'request' field MUST be a faithful one-line summary of the user input. Suggestion: %q (use this or paraphrase — but DO NOT use placeholders like \"clarification_required\" or \"no_changes_required\").\n", summary)
	}
	sb.WriteString("5. Cite format: paths use forward slashes ('/'), exactly as they appeared in the RETRIEVED CONTEXT. Do NOT replace slashes with underscores. Example correct cite: core-api/multi-tenant-data/.../Foo.java#L1-L80 — NOT core_api_multi_tenant_data...\n")
	sb.WriteString("\nProduce the corrected verdict.\n")
	return sb.String()
}
