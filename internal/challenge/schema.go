package challenge

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
