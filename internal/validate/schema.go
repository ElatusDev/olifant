// Package validate implements the post-Claude validator role: given a
// claim from Claude (natural-language summary of what it did) and the
// evidence (a git diff), determine which claims are evidenced, which
// aren't, and whether platform standards were honored.
//
// Output is grammar-constrained JSON matching the schema below; the
// validate subcommand renders it as YAML for human display.
package validate

import "regexp"

// ValidateJSONSchema is the static base schema. Use BuildSchema() to inject
// per-scope dictionary enums.
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
					"items": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"claim_id", "verdict", "evidence"},
						"properties": map[string]interface{}{
							"claim_id": map[string]interface{}{"type": "string"},
							"verdict": map[string]interface{}{
								"type": "string",
								"enum": []string{"evidenced", "partial", "unmatched"},
							},
							"evidence": map[string]interface{}{"type": "string"},
						},
					},
				},
				"standards_satisfied": map[string]interface{}{"type": "array", "maxItems": 8, "items": map[string]interface{}{"type": "string"}},
				"standards_violated":  map[string]interface{}{"type": "array", "maxItems": 8, "items": map[string]interface{}{"type": "string"}},
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

// Compiled regexes for dictionary-term categorisation (mirrors challenge).
var (
	ReStandardID    = regexp.MustCompile(`^([A-Z]{1,5}-)+[A-Z0-9]+$`)
	ReAntiPatternID = regexp.MustCompile(`^(AP\d+|(AP|AB[BCDOSET]|AW[CHRSTBA]|AM[CPSNHE])(?:TA)?-\d+)$`)
)
