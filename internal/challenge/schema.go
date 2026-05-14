package challenge

import "regexp"

// BuildChallengeSchema returns the challenge JSON Schema with dynamic enum
// constraints injected for the four applicable_rules slots, when a validator
// is supplied. Enums are populated per-scope so the grammar layer rejects
// values that don't appear in our dictionary at decode time — eliminating
// generic-category hallucination at the SCHEMA level (not just retry).
//
// Slot → source:
//   applicable_rules.patterns               → concept names for the scope
//   applicable_rules.standards              → dictionary terms matching ^<UPPER>-\d+$ (SB-04, WA-L, TBU-05, …)
//   applicable_rules.anti_patterns_to_avoid → dictionary terms matching ^AP\d+$ or ^<UPPER>+-\d+$ (AP3, ABS-01, AMS-02, …)
//   applicable_rules.decisions_to_honor     → dictionary terms matching ^D\d+$
//
// When validator is nil, returns the static challengeJSONSchema unchanged.
func BuildChallengeSchema(v *CiteValidator, requestScopes []string) map[string]interface{} {
	if v == nil {
		return challengeJSONSchema
	}

	conceptTerms := v.TermsForScopes(LayerConcept, requestScopes)
	dictAll := v.dictionaryTermsAcrossScopes(requestScopes)
	standards := filterByPattern(dictAll, reStandardID)
	antiPatterns := filterByPattern(dictAll, reAntiPatternID)
	decisions := filterByPattern(dictAll, reDecisionID)

	return map[string]interface{}{
		"type":                 "object",
		"required":             []string{"challenge"},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"challenge": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"request", "verdict",
					"confirms", "contradicts", "clarify",
					"applicable_rules", "proceed",
				},
				"properties": map[string]interface{}{
					"request": map[string]interface{}{"type": "string"},
					"verdict": map[string]interface{}{
						"type": "string",
						"enum": []string{
							"VALID", "VALID_WITH_CAVEATS", "INVALID",
							"NEEDS_CLARIFICATION", "OUT_OF_SCOPE",
						},
					},
					"confirms":    confirmsSchema(),
					"contradicts": contradictsSchema(),
					"clarify":     claritySchema(),
					"applicable_rules": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"required": []string{
							"standards", "patterns",
							"anti_patterns_to_avoid", "decisions_to_honor",
						},
						"properties": map[string]interface{}{
							"standards":              enumArrayOrEmpty(standards),
							"patterns":               enumArrayOrEmpty(conceptTerms),
							"anti_patterns_to_avoid": enumArrayOrEmpty(antiPatterns),
							"decisions_to_honor":     enumArrayOrEmpty(decisions),
						},
					},
					"proceed": map[string]interface{}{
						"type": "string",
						"enum": []string{"proceed_directly", "confirm_with_user", "abort"},
					},
				},
				"allOf": []interface{}{
					ifVerdictThenProceed("VALID", "proceed_directly"),
					ifVerdictThenProceed("VALID_WITH_CAVEATS", "confirm_with_user"),
					ifVerdictThenProceed("INVALID", "abort"),
					ifVerdictThenProceed("NEEDS_CLARIFICATION", "confirm_with_user"),
					ifVerdictThenProceed("OUT_OF_SCOPE", "abort"),
				},
			},
		},
	}
}

// enumArrayOrEmpty returns a schema for an array of strings where each item
// must be drawn from `values`. If values is empty, returns an array schema
// that accepts only an empty list (forcing the model to use [] rather than
// inventing strings).
func enumArrayOrEmpty(values []string) map[string]interface{} {
	if len(values) == 0 {
		return map[string]interface{}{
			"type":     "array",
			"maxItems": 0,
		}
	}
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type": "string",
			"enum": values,
		},
	}
}

// filterByPattern keeps only the terms that match `re`.
func filterByPattern(terms []string, re *regexp.Regexp) []string {
	if len(terms) == 0 {
		return nil
	}
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if re.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

// Pattern dictionaries for the three artifact-ID slots.
var (
	// SB-04, SI-05, WA-L, WA-CA-A, TBU-05, TWU-12, TMU-08, OL-04, OE-12, etc.
	// Excludes D### and AP## which have their own slots.
	reStandardID = regexp.MustCompile(`^([A-Z]{1,5}-)+[A-Z0-9]+$`)
	// AP3 (top-level catalog) OR stack-specific (ABS-01, AMS-02, AWC-04, ABB-06, …)
	reAntiPatternID = regexp.MustCompile(`^(AP\d+|(AP|AB[BCDOSET]|AW[CHRSTBA]|AM[CPSNHE])(?:TA)?-\d+)$`)
	reDecisionID    = regexp.MustCompile(`^D\d+$`)
)

// challengeJSONSchema is the JSON Schema passed to Ollama's `format` field for
// grammar-restricted decoding. The model is constrained at generation time to
// emit only output that satisfies this schema — eliminating the "schema escape"
// failure mode where the model imitates output formats from retrieved chunks.
//
// The schema also binds `verdict` ↔ `proceed` via `allOf`+`if/then` so the
// model can't pair INVALID with proceed_directly, etc.
//
// Coupling rules:
//   VALID                 → proceed_directly
//   VALID_WITH_CAVEATS    → confirm_with_user
//   INVALID               → abort
//   NEEDS_CLARIFICATION   → confirm_with_user
//   OUT_OF_SCOPE          → abort
var challengeJSONSchema = map[string]interface{}{
	"type":                 "object",
	"required":             []string{"challenge"},
	"additionalProperties": false,
	"properties": map[string]interface{}{
		"challenge": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"request", "verdict",
				"confirms", "contradicts", "clarify",
				"applicable_rules", "proceed",
			},
			"properties": map[string]interface{}{
				"request": map[string]interface{}{"type": "string"},
				"verdict": map[string]interface{}{
					"type": "string",
					"enum": []string{
						"VALID",
						"VALID_WITH_CAVEATS",
						"INVALID",
						"NEEDS_CLARIFICATION",
						"OUT_OF_SCOPE",
					},
				},
				"confirms":    confirmsSchema(),
				"contradicts": contradictsSchema(),
				"clarify":     claritySchema(),
				"applicable_rules": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": false,
					"required": []string{
						"standards", "patterns",
						"anti_patterns_to_avoid", "decisions_to_honor",
					},
					"properties": map[string]interface{}{
						"standards":              stringArray(),
						"patterns":               stringArray(),
						"anti_patterns_to_avoid": stringArray(),
						"decisions_to_honor":     stringArray(),
					},
				},
				"proceed": map[string]interface{}{
					"type": "string",
					"enum": []string{
						"proceed_directly",
						"confirm_with_user",
						"abort",
					},
				},
			},
			"allOf": []interface{}{
				ifVerdictThenProceed("VALID", "proceed_directly"),
				ifVerdictThenProceed("VALID_WITH_CAVEATS", "confirm_with_user"),
				ifVerdictThenProceed("INVALID", "abort"),
				ifVerdictThenProceed("NEEDS_CLARIFICATION", "confirm_with_user"),
				ifVerdictThenProceed("OUT_OF_SCOPE", "abort"),
			},
		},
	},
}

// ifVerdictThenProceed builds one if/then clause for the challenge object's
// allOf: if verdict == <v>, then proceed must == <p>.
func ifVerdictThenProceed(verdict, proceed string) map[string]interface{} {
	return map[string]interface{}{
		"if": map[string]interface{}{
			"properties": map[string]interface{}{
				"verdict": map[string]interface{}{"const": verdict},
			},
		},
		"then": map[string]interface{}{
			"properties": map[string]interface{}{
				"proceed": map[string]interface{}{"const": proceed},
			},
		},
	}
}

func confirmsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"claim", "cites"},
			"properties": map[string]interface{}{
				"claim": map[string]interface{}{"type": "string"},
				"cites": stringArray(),
			},
		},
	}
}

func contradictsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"claim", "counter", "cites"},
			"properties": map[string]interface{}{
				"claim":   map[string]interface{}{"type": "string"},
				"counter": map[string]interface{}{"type": "string"},
				"cites":   stringArray(),
			},
		},
	}
}

func claritySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"question", "why_asking"},
			"properties": map[string]interface{}{
				"question":   map[string]interface{}{"type": "string"},
				"why_asking": map[string]interface{}{"type": "string"},
			},
		},
	}
}

func stringArray() map[string]interface{} {
	return map[string]interface{}{
		"type":  "array",
		"items": map[string]interface{}{"type": "string"},
	}
}
