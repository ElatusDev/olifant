package validate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateJSONSchema_RequiresCitesInClaimAssessments(t *testing.T) {
	// Walk to validate.properties.claim_assessments.items and assert cites is required.
	root := ValidateJSONSchema["properties"].(map[string]interface{})
	validateObj := root["validate"].(map[string]interface{})
	props := validateObj["properties"].(map[string]interface{})
	ca := props["claim_assessments"].(map[string]interface{})
	items := ca["items"].(map[string]interface{})
	req := items["required"].([]string)
	found := false
	for _, r := range req {
		if r == "cites" {
			found = true
		}
	}
	if !found {
		t.Fatalf("claim_assessments items must require 'cites', got %v", req)
	}
}

func TestValidateJSONSchema_VerdictProceedAllOfCoupling(t *testing.T) {
	root := ValidateJSONSchema["properties"].(map[string]interface{})
	validateObj := root["validate"].(map[string]interface{})
	allOf := validateObj["allOf"].([]interface{})
	if len(allOf) != 3 {
		t.Fatalf("expected 3 if/then coupling clauses, got %d", len(allOf))
	}
	// Sanity: schema marshals to JSON cleanly (consumed by Ollama format field).
	b, err := json.Marshal(ValidateJSONSchema)
	if err != nil {
		t.Fatalf("schema not JSON-marshalable: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"overall_verdict"`,
		`"const":"validated"`,
		`"const":"merge"`,
		`"const":"partial"`,
		`"const":"hold"`,
		`"const":"failed"`,
		`"const":"block"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("schema JSON missing %q", want)
		}
	}
}

func TestEnumArrayOrEmpty_EmptyValues_OnlyAcceptsEmptyArray(t *testing.T) {
	got := enumArrayOrEmpty(nil, 8)
	if got["type"] != "array" {
		t.Fatalf("type should be array, got %v", got["type"])
	}
	if got["maxItems"] != 0 {
		t.Fatalf("maxItems must be 0 when no values, got %v", got["maxItems"])
	}
}

func TestEnumArrayOrEmpty_NonEmptyValues_HasItemsEnum(t *testing.T) {
	got := enumArrayOrEmpty([]string{"D17", "AP3"}, 8)
	if got["maxItems"] != 8 {
		t.Fatalf("maxItems should be 8, got %v", got["maxItems"])
	}
	items := got["items"].(map[string]interface{})
	enum := items["enum"].([]string)
	if len(enum) != 2 || enum[0] != "D17" || enum[1] != "AP3" {
		t.Fatalf("enum mismatch, got %v", enum)
	}
}

func TestFilterByPattern_StandardID(t *testing.T) {
	terms := []string{"D17", "AP3", "SB-04", "WA-L", "TBU-05", "not_a_term"}
	got := filterByPattern(terms, ReStandardID)
	want := map[string]bool{"SB-04": true, "WA-L": true, "TBU-05": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d standard IDs, got %d (%v)", len(want), len(got), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected term %s in standards filter", g)
		}
	}
}

func TestBuildValidateSchema_NilValidator_ReturnsBaseSchema(t *testing.T) {
	got := BuildValidateSchema(nil, nil)
	if got == nil {
		t.Fatalf("got nil schema")
	}
	// JSON-marshal both and compare structurally.
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(ValidateJSONSchema)
	if string(a) != string(b) {
		t.Fatalf("BuildValidateSchema(nil,nil) should equal ValidateJSONSchema")
	}
}
