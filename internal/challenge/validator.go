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

// Layer identifies which abstraction-layer directory a term came from. Used
// for surgical retry hints ("you cited a pattern — here are the legal
// concept names").
type Layer string

const (
	LayerDictionary  Layer = "dictionary"  // dictionary/<scope>/<file>.yaml — artifact IDs (D###, AP##, SB-##, …)
	LayerConcept     Layer = "concept"     // concepts/concepts.yaml
	LayerConstraint  Layer = "constraint"  // constraints/constraints.yaml
	LayerGlossary    Layer = "glossary"    // glossary/glossary.yaml
)

// CiteValidator holds the corpus of legal terms (across all layers and
// scopes) and file path prefixes. Reusable across many calls.
type CiteValidator struct {
	// knownTerms is the union — fast O(1) lookup for cite resolution.
	knownTerms map[string]struct{}
	// termsByLayerScope: [layer][scope][]term  — for surgical, scope-aware
	// retry hints. `business` is the apex scope, always included.
	termsByLayerScope map[Layer]map[string][]string
	platformRoot      string
	repoPrefixes      []string
}

// ApexScope is the business-domain layer that's always loaded regardless of
// the request's scope filter.
const ApexScope = "business"

// NewCiteValidator loads every layer under kbRoot/{dictionary,concepts,
// constraints,glossary} into a per-layer per-scope index. Each YAML file's
// scope is derived from its parent directory name (e.g.,
// concepts/backend/concepts.yaml → scope "backend"). Files at a layer's
// root (no scope dir) are treated as scope "universal" for backward
// compatibility with the old flat layout.
func NewCiteValidator(platformRoot, kbRoot string) (*CiteValidator, error) {
	v := &CiteValidator{
		knownTerms:        map[string]struct{}{},
		termsByLayerScope: map[Layer]map[string][]string{},
		platformRoot:      platformRoot,
		repoPrefixes: []string{
			"core-api", "akademia-plus-web", "elatusdev-web",
			"akademia-plus-central", "akademia-plus-go",
			"core-api-e2e", "infra",
			"knowledge-base",
			"decisions", "anti-patterns", "standards", "patterns",
			"workflows", "prompts", "retrospectives", "templates",
			"skills", "operations", "architecture", "audit-report",
			"ux", "views", "concepts", "constraints", "glossary", "dsl",
		},
	}

	type layerSource struct {
		layer Layer
		path  string
	}
	sources := []layerSource{
		{LayerDictionary, filepath.Join(kbRoot, "dictionary")},
		{LayerConcept, filepath.Join(kbRoot, "concepts")},
		{LayerConstraint, filepath.Join(kbRoot, "constraints")},
		{LayerGlossary, filepath.Join(kbRoot, "glossary")},
	}

	for _, src := range sources {
		if _, statErr := os.Stat(src.path); statErr != nil {
			continue
		}
		if err := filepath.Walk(src.path, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			// Scope is the immediate parent directory under the layer
			// root (e.g., concepts/backend/concepts.yaml → "backend").
			// If no scope dir (file directly under layer root), use ApexScope.
			scope := ApexScope
			if rel, rErr := filepath.Rel(src.path, path); rErr == nil {
				if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) > 1 {
					scope = parts[0]
				}
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
			if _, ok := v.termsByLayerScope[src.layer]; !ok {
				v.termsByLayerScope[src.layer] = map[string][]string{}
			}
			for _, e := range entries {
				if e.Term != "" {
					if _, dup := v.knownTerms[e.Term]; !dup {
						v.termsByLayerScope[src.layer][scope] = append(v.termsByLayerScope[src.layer][scope], e.Term)
					}
					v.knownTerms[e.Term] = struct{}{}
				}
				for _, s := range e.Synonyms {
					v.knownTerms[s] = struct{}{}
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Stable order for retry-hint enumeration.
	for layer := range v.termsByLayerScope {
		for scope := range v.termsByLayerScope[layer] {
			sort.Strings(v.termsByLayerScope[layer][scope])
		}
	}
	return v, nil
}

// KnownCount returns the total number of unique terms loaded (informational).
func (v *CiteValidator) KnownCount() int { return len(v.knownTerms) }

// CountByLayer returns the count of terms loaded for a layer across all scopes.
func (v *CiteValidator) CountByLayer(l Layer) int {
	n := 0
	for _, terms := range v.termsByLayerScope[l] {
		n += len(terms)
	}
	return n
}

// TermsForScopes returns, for each layer, the union of terms from the apex
// (`business`) scope plus the request's specific scopes. Used by retry-hint
// composition to keep enumerations focused.
func (v *CiteValidator) TermsForScopes(layer Layer, requestScopes []string) []string {
	scopes := append([]string{ApexScope}, requestScopes...)
	scopeMap := v.termsByLayerScope[layer]
	if scopeMap == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range scopes {
		for _, t := range scopeMap[s] {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

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

// resolves returns true if `c` is a known term from any abstraction layer
// (dictionary, concepts, constraints, glossary) OR a real platform file
// path (with optional #anchor or #Lstart-Lend suffix).
func (v *CiteValidator) resolves(c string) bool {
	c = strings.TrimSpace(c)
	if c == "" {
		return false
	}
	base := c
	if hashIdx := strings.IndexByte(c, '#'); hashIdx >= 0 {
		base = c[:hashIdx]
	}
	if _, ok := v.knownTerms[base]; ok {
		return true
	}
	if _, ok := v.knownTerms[c]; ok {
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
// `originalRequest` lets the model copy the request verbatim if needed.
// `requestScopes` filters retry hints — the enumeration includes only the
// apex (`business`) layer plus the request's scopes.
func (v *CiteValidator) RetryPromptAddendum(violations []Violation, originalRequest string, requestScopes []string) string {
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
	sb.WriteString("5. Cite format: paths use forward slashes ('/'), exactly as they appeared in the RETRIEVED CONTEXT. Do NOT replace slashes with underscores. Example correct cite: core-api/multi-tenant-data/.../Foo.java#L1-L80 — NOT core_api_multi_tenant_data...\n\n")

	// Enumerate the SMALL per-layer term sets so the model has a concrete
	// allow-list. Dictionary has 800+ entries (too many to enumerate);
	// scope dictionary citations to artifact IDs from RETRIEVED CONTEXT.
	// Enumeration includes apex (`business`) + the request's scopes.
	if concepts := v.TermsForScopes(LayerConcept, requestScopes); len(concepts) > 0 {
		fmt.Fprintf(&sb, "LEGAL CONCEPT NAMES (use these — or leave the slot empty — never invent new ones):\n  %s\n\n",
			strings.Join(concepts, ", "))
	}
	if constraints := v.TermsForScopes(LayerConstraint, requestScopes); len(constraints) > 0 {
		fmt.Fprintf(&sb, "LEGAL CONSTRAINT NAMES (cite these in contradicts when the request violates a hard rule):\n  %s\n\n",
			strings.Join(constraints, ", "))
	}
	if glossary := v.TermsForScopes(LayerGlossary, requestScopes); len(glossary) > 0 {
		fmt.Fprintf(&sb, "LEGAL GLOSSARY TERMS (canonical handles for platform aliases):\n  %s\n\n",
			strings.Join(glossary, ", "))
	}
	sb.WriteString("For artifact IDs (D###, AP##, SB-##, etc.) and file paths: use ONLY those that appear in the RETRIEVED CONTEXT above.\n\n")
	sb.WriteString("Produce the corrected verdict.\n")
	return sb.String()
}
