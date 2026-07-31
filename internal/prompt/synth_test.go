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

// #122 S3: the cache-relevant layout property — stable prefix first. Pinned
// by ORDER and ABSENCE assertions, not golden prose bytes (workflow D-5).
func TestBuildPromptText_StablePrefixFirstLayout(t *testing.T) {
	hits := []Hit{
		{Doc: "chunk one body\n", Distance: 0.3121, Scope: "backend/corpus", Meta: map[string]interface{}{"source": "a.md"}},
		{Doc: "chunk two body\n", Distance: 0.487, Scope: "universal/corpus", Meta: map[string]interface{}{"source": "b.md"}},
	}
	out := buildPromptText("volatile goal text", hits)

	ctxIdx := strings.Index(out, "RETRIEVED CONTEXT")
	distIdx := strings.Index(out, "CHUNK DISTANCES")
	goalIdx := strings.Index(out, "USER GOAL:")
	if ctxIdx == -1 || distIdx == -1 || goalIdx == -1 {
		t.Fatalf("missing section marker(s): ctx=%d dist=%d goal=%d\n%s", ctxIdx, distIdx, goalIdx, out)
	}
	if ctxIdx >= distIdx || distIdx >= goalIdx {
		t.Errorf("layout order broken: want chunks < distances < goal, got ctx=%d dist=%d goal=%d", ctxIdx, distIdx, goalIdx)
	}
	// AC1: nothing per-call-volatile before the goal marker except the
	// distances trailer — specifically no inline distance= header floats.
	if strings.Contains(out[:goalIdx], "distance=") {
		t.Errorf("inline distance= float inside the cached prefix (AC1):\n%s", out[:goalIdx])
	}
	// Trailer carries one entry per hit.
	trailerLine := out[distIdx : strings.Index(out[distIdx:], "\n")+distIdx]
	for _, want := range []string{"1=0.3121", "2=0.4870"} {
		if !strings.Contains(trailerLine, want) {
			t.Errorf("distances trailer missing %q: %q", want, trailerLine)
		}
	}
}

func TestBuildPromptText_ZeroHitsShape(t *testing.T) {
	out := buildPromptText("some goal", nil)
	if strings.Contains(out, "CHUNK DISTANCES") {
		t.Errorf("distances trailer present with zero hits:\n%s", out)
	}
	goalIdx := strings.Index(out, "USER GOAL:")
	ctxIdx := strings.Index(out, "RETRIEVED CONTEXT")
	if ctxIdx == -1 || goalIdx == -1 || ctxIdx > goalIdx {
		t.Errorf("zero-hit layout broken (ctx=%d goal=%d):\n%s", ctxIdx, goalIdx, out)
	}
	if !strings.Contains(out, "PRODUCE THE PROMPT-STEP PLAN") {
		t.Errorf("instruction tail missing:\n%s", out)
	}
}
