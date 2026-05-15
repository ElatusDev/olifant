package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPromptText_IncludesGoalVerbatim(t *testing.T) {
	goal := "Add a /healthz endpoint with multitenancy"
	out := buildPromptText(goal, nil)
	if !strings.Contains(out, "USER GOAL:") {
		t.Errorf("prompt missing USER GOAL header:\n%s", out)
	}
	if !strings.Contains(out, goal) {
		t.Errorf("prompt missing verbatim goal:\n%s", out)
	}
}

func TestBuildPromptText_EmbedsHitMetadata(t *testing.T) {
	hits := []Hit{
		{
			Doc:      "TenantScoped pattern requires composite key (tenantId, entityId).\n",
			Distance: 0.42,
			Scope:    "backend/corpus",
			Meta: map[string]interface{}{
				"source":        "knowledge-base/patterns/backend.md",
				"source_anchor": "backend.md#tenant-scoped",
				"artifact_id":   "AP3",
			},
		},
	}
	out := buildPromptText("add invoice entity", hits)
	for _, frag := range []string{
		"backend/corpus", "AP3", "backend.md#tenant-scoped",
		"composite key", "PRODUCE THE PROMPT-STEP PLAN",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("prompt missing %q in:\n%s", frag, out)
		}
	}
}

func TestBuildPromptText_FallsBackToSourceWhenAnchorMissing(t *testing.T) {
	hits := []Hit{
		{
			Doc:      "ARC source body",
			Distance: 0.3,
			Scope:    "infra/corpus",
			Meta:     map[string]interface{}{"source": "infra/main.tf"},
		},
	}
	out := buildPromptText("add IAM role", hits)
	if !strings.Contains(out, "source=infra/main.tf") {
		t.Errorf("expected source= breadcrumb when no anchor, got:\n%s", out)
	}
}

func TestPlanSynthSchema_StepsArrayShape(t *testing.T) {
	schema := planSynthSchema()
	plan := schema["properties"].(map[string]interface{})["plan"].(map[string]interface{})
	steps := plan["properties"].(map[string]interface{})["steps"].(map[string]interface{})
	if steps["type"].(string) != "array" {
		t.Errorf("steps.type = %v, want array", steps["type"])
	}
	if _, ok := steps["items"].(map[string]interface{}); !ok {
		t.Errorf("steps.items must be a map; got %T", steps["items"])
	}
	// Hard constraint: we deliberately do NOT emit pattern/min/max/enum here.
	// Those crash Ollama's grammar engine on nested schemas.
	for _, banned := range []string{"pattern", "minItems", "maxItems", "minLength", "maxLength", "enum"} {
		if _, found := steps[banned]; found {
			t.Errorf("steps must not include %q — see comment in planSynthSchema", banned)
		}
	}
}

func TestStepSynthSchema_HasStructuralFieldsOnly(t *testing.T) {
	step := stepSynthSchema()
	required, ok := step["required"].([]string)
	if !ok {
		t.Fatalf("required missing: %T", step["required"])
	}
	wantRequired := map[string]bool{"id": true, "name": true, "description": true, "expected_output": true}
	for _, r := range required {
		if !wantRequired[r] {
			t.Errorf("unexpected required field: %s", r)
		}
	}
	// No pattern constraints in step.properties.id either.
	props := step["properties"].(map[string]interface{})
	id := props["id"].(map[string]interface{})
	if _, has := id["pattern"]; has {
		t.Error("id.pattern is forbidden — enforce step_NN in Go after parse")
	}
}

func TestPlanSynthSchema_RoundTripsAsJSON(t *testing.T) {
	// The schema is sent over the wire as Ollama's `format` field — must
	// marshal cleanly as JSON.
	schema := planSynthSchema()
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("planSynthSchema does not marshal as JSON: %v", err)
	}
}

func TestPlanSynthSchema_ScopeIsStringArrayNoEnum(t *testing.T) {
	// scope.items.enum was removed because it crashed Ollama. Scope
	// validation now happens in Go post-parse — see transformSynthJSONToPlan.
	schema := planSynthSchema()
	plan := schema["properties"].(map[string]interface{})["plan"].(map[string]interface{})
	scope := plan["properties"].(map[string]interface{})["scope"].(map[string]interface{})
	items := scope["items"].(map[string]interface{})
	if _, hasEnum := items["enum"]; hasEnum {
		t.Error("scope.items.enum forbidden — see comment in planSynthSchema")
	}
	if items["type"].(string) != "string" {
		t.Errorf("scope.items.type = %v, want string", items["type"])
	}
}

func TestSystemPrompt_NamesPSPAndRequiredFields(t *testing.T) {
	// Cheap canary — if these strings drift away from the synth contract,
	// downstream parsing breaks silently.
	for _, frag := range []string{
		"Prompt-Step Protocol",
		"step_01",
		"depends_on",
		"expected_output",
		"signals",
	} {
		if !strings.Contains(systemPrompt, frag) {
			t.Errorf("systemPrompt missing required fragment %q", frag)
		}
	}
}
