// Package validate implements the post-Claude validator role: given a
// claim from Claude (natural-language summary of what it did) and the
// evidence (a git diff), determine which claims are evidenced, which
// aren't, and whether platform standards were honored.
//
// Output is grammar-constrained JSON matching the schema below; the
// validate subcommand renders it as YAML for human display.
package validate

import (
	"regexp"

	"github.com/ElatusDev/olifant/internal/challenge"
)

// ValidateJSONSchema is the static base schema. Use BuildValidateSchema() to
// inject per-scope dictionary enums into standards_satisfied / standards_violated.
//
// Schema requires claim_assessments[].cites — at the grammar layer every
// assessment must include the cites slot (may be empty). The post-validation
// AssessmentValidator rejects empty cites when the verdict is evidenced or
// partial; cites may remain empty for unmatched (where "no evidence" is
// the correct answer).
var ValidateJSONSchema = map[string]interface{}{
	"type":                 "object",
	"required":             []string{"validate"},
	"additionalProperties": false,
	"properties": map[string]interface{}{
		"validate": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"claim_summary", "claims_parsed", "claim_assessments",
				"standards_satisfied", "standards_violated",
				"overall_verdict", "proceed",
			},
			"properties": map[string]interface{}{
				"claim_summary": map[string]interface{}{"type": "string"},
				"claims_parsed": map[string]interface{}{
					"type":     "array",
					"maxItems": 16,
					"items": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"id", "text"},
						"properties": map[string]interface{}{
							"id":   map[string]interface{}{"type": "string"},
							"text": map[string]interface{}{"type": "string"},
						},
					},
				},
				"claim_assessments": map[string]interface{}{
					"type":     "array",
					"maxItems": 16,
					"items":    claimAssessmentItemSchema(),
				},
				"standards_satisfied": stringArray(8),
				"standards_violated":  stringArray(8),
				"overall_verdict": map[string]interface{}{
					"type": "string",
					"enum": []string{"validated", "partial", "failed"},
				},
				"proceed": map[string]interface{}{
					"type": "string",
					"enum": []string{"merge", "hold", "block"},
				},
			},
			"allOf": []interface{}{
				ifVerdictThenProceed("validated", "merge"),
				ifVerdictThenProceed("partial", "hold"),
				ifVerdictThenProceed("failed", "block"),
			},
		},
	},
}

// BuildValidateSchema returns the validate schema with dictionary-enum
// constraints injected into standards_satisfied and standards_violated when
// a CiteValidator is supplied. Cites inside claim_assessments stay free-form
// because they typically reference diff-file paths (e.g.,
// core-api/foo.java#L42-L60) that the dictionary doesn't enumerate.
//
// When cv is nil, returns the static ValidateJSONSchema unchanged.
func BuildValidateSchema(cv *challenge.CiteValidator, requestScopes []string) map[string]interface{} {
	if cv == nil {
		return ValidateJSONSchema
	}

	standards := filterByPattern(allDictionaryTerms(cv), ReStandardID)

	return map[string]interface{}{
		"type":                 "object",
		"required":             []string{"validate"},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"validate": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"claim_summary", "claims_parsed", "claim_assessments",
					"standards_satisfied", "standards_violated",
					"overall_verdict", "proceed",
				},
				"properties": map[string]interface{}{
					"claim_summary": map[string]interface{}{"type": "string"},
					"claims_parsed": map[string]interface{}{
						"type":     "array",
						"maxItems": 16,
						"items": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"id", "text"},
							"properties": map[string]interface{}{
								"id":   map[string]interface{}{"type": "string"},
								"text": map[string]interface{}{"type": "string"},
							},
						},
					},
					"claim_assessments": map[string]interface{}{
						"type":     "array",
						"maxItems": 16,
						"items":    claimAssessmentItemSchema(),
					},
					"standards_satisfied": enumArrayOrEmpty(standards, 8),
					"standards_violated":  enumArrayOrEmpty(standards, 8),
					"overall_verdict": map[string]interface{}{
						"type": "string",
						"enum": []string{"validated", "partial", "failed"},
					},
					"proceed": map[string]interface{}{
						"type": "string",
						"enum": []string{"merge", "hold", "block"},
					},
				},
				"allOf": []interface{}{
					ifVerdictThenProceed("validated", "merge"),
					ifVerdictThenProceed("partial", "hold"),
					ifVerdictThenProceed("failed", "block"),
				},
			},
		},
	}
}

func claimAssessmentItemSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"claim_id", "verdict", "evidence", "cites"},
		"properties": map[string]interface{}{
			"claim_id": map[string]interface{}{"type": "string"},
			"verdict": map[string]interface{}{
				"type": "string",
				"enum": []string{"evidenced", "partial", "unmatched"},
			},
			"evidence": map[string]interface{}{"type": "string"},
			"cites":    stringArray(8),
		},
	}
}

func ifVerdictThenProceed(verdict, proceed string) map[string]interface{} {
	return map[string]interface{}{
		"if": map[string]interface{}{
			"properties": map[string]interface{}{
				"overall_verdict": map[string]interface{}{"const": verdict},
			},
		},
		"then": map[string]interface{}{
			"properties": map[string]interface{}{
				"proceed": map[string]interface{}{"const": proceed},
			},
		},
	}
}

func stringArray(maxItems int) map[string]interface{} {
	return map[string]interface{}{
		"type":     "array",
		"maxItems": maxItems,
		"items":    map[string]interface{}{"type": "string"},
	}
}

// enumArrayOrEmpty constrains an array to a closed enum (or only-empty when
// values is empty). Mirrors challenge.enumArrayOrEmpty.
func enumArrayOrEmpty(values []string, maxItems int) map[string]interface{} {
	if len(values) == 0 {
		return map[string]interface{}{
			"type":     "array",
			"maxItems": 0,
		}
	}
	return map[string]interface{}{
		"type":     "array",
		"maxItems": maxItems,
		"items": map[string]interface{}{
			"type": "string",
			"enum": values,
		},
	}
}

// filterByPattern keeps only values matching re.
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

// allDictionaryTerms returns every dictionary-layer term across all scopes
// (artifact IDs are platform-global, no per-stack partition).
func allDictionaryTerms(cv *challenge.CiteValidator) []string {
	if cv == nil {
		return nil
	}
	terms := cv.TermsForScopes(challenge.LayerDictionary, []string{"backend", "webapp", "mobile", "e2e", "infra", "platform-process"})
	return terms
}

// Compiled regexes for dictionary-term categorisation (mirrors challenge).
var (
	ReStandardID    = regexp.MustCompile(`^([A-Z]{1,5}-)+[A-Z0-9]+$`)
	ReAntiPatternID = regexp.MustCompile(`^(AP\d+|(AP|AB[BCDOSET]|AW[CHRSTBA]|AM[CPSNHE])(?:TA)?-\d+)$`)
)
