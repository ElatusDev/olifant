package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/psp"
)

func TestGeneratePlanID_FormatMatchesPSPv1Example(t *testing.T) {
	ts := time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC)
	got := generatePlanID(ts, "Add a TenantScoped entity for invoices")
	// PSP v1 §3.1 example: 2026-05-14T20-15-00Z-abc123
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z-[0-9a-f]{6}$`)
	if !re.MatchString(got) {
		t.Errorf("plan_id %q does not match PSP v1 shape", got)
	}
	if !strings.HasPrefix(got, "2026-05-14T20-15-00Z-") {
		t.Errorf("plan_id %q does not embed the supplied timestamp", got)
	}
}

func TestGeneratePlanID_DeterministicForSameTsAndSeed(t *testing.T) {
	ts := time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC)
	a := generatePlanID(ts, "smoke")
	b := generatePlanID(ts, "smoke")
	if a != b {
		t.Errorf("plan_id is non-deterministic: %s vs %s", a, b)
	}
}

func TestGeneratePlanID_DifferentSeedsDiverge(t *testing.T) {
	ts := time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC)
	if generatePlanID(ts, "a") == generatePlanID(ts, "b") {
		t.Error("different seeds must produce different plan_ids")
	}
}

func TestApplyDefaults_FillsZeroFields(t *testing.T) {
	got := applyDefaults(Config{})
	if got.TopN != 8 {
		t.Errorf("default TopN = %d, want 8", got.TopN)
	}
	if got.MaxTokens != 1024 {
		t.Errorf("default MaxTokens = %d, want 1024", got.MaxTokens)
	}
	if got.OutDir != "plans" {
		t.Errorf("default OutDir = %q, want %q", got.OutDir, "plans")
	}
}

func TestApplyDefaults_RespectsUserOverrides(t *testing.T) {
	got := applyDefaults(Config{TopN: 4, MaxTokens: 1500, OutDir: "/tmp/custom"})
	if got.TopN != 4 || got.MaxTokens != 1500 || got.OutDir != "/tmp/custom" {
		t.Errorf("user overrides clobbered: %+v", got)
	}
}

func TestSynthExpectedToSchema_ObjectWithFields(t *testing.T) {
	got := synthExpectedToSchema("object", []string{"summary", "rationale"})
	if got["type"] != "object" {
		t.Errorf("type = %v, want object", got["type"])
	}
	required, ok := got["required"].([]string)
	if !ok {
		t.Fatalf("required missing or wrong type: %T", got["required"])
	}
	// required is sorted for determinism
	want := []string{"rationale", "summary"}
	if !reflect.DeepEqual(required, want) {
		t.Errorf("required = %v, want %v (sorted)", required, want)
	}
	props, ok := got["properties"].(map[string]interface{})
	if !ok || len(props) != 2 {
		t.Errorf("properties = %v, expected map with 2 entries", got["properties"])
	}
}

func TestSynthExpectedToSchema_DefaultsTypeWhenEmpty(t *testing.T) {
	got := synthExpectedToSchema("", nil)
	if got["type"] != "object" {
		t.Errorf("empty type should default to object, got %v", got["type"])
	}
	if _, ok := got["properties"]; ok {
		t.Errorf("no fields supplied → no properties key, got %v", got)
	}
}

func TestTransformSynthJSONToPlan_HappyPath(t *testing.T) {
	raw := `{
		"plan": {
			"goal": "Add a /healthz endpoint",
			"scope": ["backend"],
			"steps": [
				{
					"id": "step_01",
					"name": "Survey existing health endpoints",
					"description": "Read core-api health probe wiring.",
					"signals": ["core-api/elatus-rest-api/src/main/java/.../HealthController.java"],
					"depends_on": [],
					"expected_output": {"type": "object", "fields": ["summary"]}
				},
				{
					"id": "step_02",
					"name": "Wire /healthz endpoint",
					"description": "Add @GetMapping(/healthz) returning 200.",
					"depends_on": ["step_01"],
					"expected_output": {"type": "object", "fields": ["java_source"]}
				}
			]
		}
	}`
	plan, err := transformSynthJSONToPlan(raw, "fallback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Goal != "Add a /healthz endpoint" {
		t.Errorf("goal = %q", plan.Goal)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(plan.Steps))
	}
	if plan.Steps[1].DependsOn[0] != "step_01" {
		t.Errorf("step_02 depends_on = %v", plan.Steps[1].DependsOn)
	}
	if plan.Steps[0].RetryPolicy.MaxAttempts != 2 {
		t.Errorf("default retry max_attempts should be 2, got %d", plan.Steps[0].RetryPolicy.MaxAttempts)
	}
}

func TestTransformSynthJSONToPlan_FallsBackOnEmptyGoal(t *testing.T) {
	raw := `{"plan": {"goal": "", "steps": [
		{"id":"step_01","name":"n","description":"d","expected_output":{"type":"object"}}
	]}}`
	plan, err := transformSynthJSONToPlan(raw, "supplied-fallback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Goal != "supplied-fallback" {
		t.Errorf("expected fallback goal, got %q", plan.Goal)
	}
}

func TestTransformSynthJSONToPlan_RejectsZeroSteps(t *testing.T) {
	raw := `{"plan": {"goal": "g", "steps": []}}`
	if _, err := transformSynthJSONToPlan(raw, "fallback"); err == nil {
		t.Error("expected error for zero-step plan")
	}
}

func TestTransformSynthJSONToPlan_RejectsBadJSON(t *testing.T) {
	if _, err := transformSynthJSONToPlan("not json", "fallback"); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestWritePlan_WritesYamlReloadableByPSP(t *testing.T) {
	tmp := t.TempDir()
	plan := &psp.Plan{
		PlanID: "2026-05-14T20-15-00Z-deadbe",
		Goal:   "smoke goal",
		Steps: []psp.Step{
			{ID: "step_01", Description: "do thing", ExpectedOutput: psp.ExpectedOutput{Schema: map[string]interface{}{"type": "object"}}},
		},
	}
	got, err := writePlan(tmp, plan)
	if err != nil {
		t.Fatalf("writePlan: %v", err)
	}
	if filepath.Base(got) != plan.PlanID+".yaml" {
		t.Errorf("filename = %s, want %s.yaml", filepath.Base(got), plan.PlanID)
	}
	// Re-read with psp.LoadPlan to confirm round-trip.
	loaded, err := psp.LoadPlan(got)
	if err != nil {
		t.Fatalf("psp.LoadPlan: %v", err)
	}
	if loaded.PlanID != plan.PlanID || loaded.Goal != plan.Goal || len(loaded.Steps) != 1 {
		t.Errorf("round-trip mismatch: %+v", loaded)
	}
	if err := psp.Validate(loaded); err != nil {
		t.Errorf("written plan failed psp.Validate: %v", err)
	}
	// Header comment is present.
	raw, _ := os.ReadFile(got)
	if !strings.HasPrefix(string(raw), "# Olifant PSP plan") {
		t.Errorf("file does not start with expected header comment:\n%s", string(raw[:80]))
	}
}

func TestBuildFromSynthJSON_SinglePlanHappyPath(t *testing.T) {
	tmp := t.TempDir()
	raw := makeSynthJSON(t, "smoke goal", 3)
	res, err := buildFromSynthJSON(raw, "smoke goal", tmp, time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildFromSynthJSON: %v", err)
	}
	if res.Split {
		t.Error("3 steps should not trigger split")
	}
	if res.StepCount != 3 {
		t.Errorf("StepCount = %d, want 3", res.StepCount)
	}
	if _, err := os.Stat(res.PlanPath); err != nil {
		t.Errorf("plan path does not exist: %v", err)
	}
	// File should validate via psp.Validate.
	loaded, lerr := psp.LoadPlan(res.PlanPath)
	if lerr != nil {
		t.Fatalf("LoadPlan: %v", lerr)
	}
	if verr := psp.Validate(loaded); verr != nil {
		t.Errorf("validate after build: %v", verr)
	}
}

func TestBuildFromSynthJSON_SplitsWhenOverCap(t *testing.T) {
	tmp := t.TempDir()
	// 27 steps with NO deps → splits into [1..25] + [26..27]. Both sub-plans
	// validate cleanly because there are no cross-boundary depends_on.
	raw := makeSynthJSONNoDeps(t, "big goal", 27)
	res, err := buildFromSynthJSON(raw, "big goal", tmp, time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildFromSynthJSON: %v", err)
	}
	if !res.Split {
		t.Error("27 steps must trigger split")
	}
	if len(res.SubPlanPaths) != 2 {
		t.Errorf("expected 2 sub-plans, got %d (%v)", len(res.SubPlanPaths), res.SubPlanPaths)
	}
	if res.StepCount != 27 {
		t.Errorf("StepCount = %d, want 27 (logical total)", res.StepCount)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected zero warnings for no-dep split, got %v", res.Warnings)
	}
	// All sub-plans must validate (no cross-boundary deps).
	for _, p := range res.SubPlanPaths {
		loaded, lerr := psp.LoadPlan(p)
		if lerr != nil {
			t.Fatalf("LoadPlan(%s): %v", p, lerr)
		}
		if verr := psp.Validate(loaded); verr != nil {
			t.Errorf("sub-plan %s failed validate: %v", p, verr)
		}
		if len(loaded.Steps) > psp.MaxStepsPerPlan {
			t.Errorf("sub-plan %s has %d steps, exceeds cap %d", p, len(loaded.Steps), psp.MaxStepsPerPlan)
		}
		if loaded.SessionID == "" {
			t.Errorf("sub-plan %s missing session_id", p)
		}
	}
}

// TestBuildFromSynthJSON_SplitRecordsWarningForCrossBoundaryDep documents the
// spec-vs-impl gap: cross-sub-plan depends_on entries are spec-legal (seeded
// at runtime) but rejected by psp.Validate. Builder records this as a
// warning rather than failing the build.
func TestBuildFromSynthJSON_SplitRecordsWarningForCrossBoundaryDep(t *testing.T) {
	tmp := t.TempDir()
	// 27 steps in a strict chain → after split, step_26 depends on step_25
	// which lives in the previous sub-plan. Validate rejects; we warn.
	raw := makeSynthJSON(t, "chained goal", 27)
	res, err := buildFromSynthJSON(raw, "chained goal", tmp, time.Date(2026, 5, 14, 20, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildFromSynthJSON: %v", err)
	}
	if !res.Split {
		t.Fatal("expected split")
	}
	if len(res.Warnings) == 0 {
		t.Error("expected at least one warning for cross-boundary dep")
	}
	// Files should still be written.
	for _, p := range res.SubPlanPaths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sub-plan %s missing: %v", p, err)
		}
	}
}

// TestValidateLogicalPlan_CatchesUnknownDep ensures the pre-split validator
// rejects a plan that references a step that doesn't exist anywhere.
func TestValidateLogicalPlan_CatchesUnknownDep(t *testing.T) {
	plan := &psp.Plan{
		Steps: []psp.Step{
			{ID: "step_01"},
			{ID: "step_02", DependsOn: []string{"step_99"}},
		},
	}
	if err := validateLogicalPlan(plan); err == nil {
		t.Error("expected error for unknown dep")
	}
}

func TestValidateLogicalPlan_CatchesDuplicateID(t *testing.T) {
	plan := &psp.Plan{
		Steps: []psp.Step{
			{ID: "step_01"},
			{ID: "step_01"},
		},
	}
	if err := validateLogicalPlan(plan); err == nil {
		t.Error("expected error for duplicate id")
	}
}

func TestValidateLogicalPlan_AllowsOverCapPlan(t *testing.T) {
	// 30 steps with no deps — psp.Validate would reject (MPS=25), but the
	// logical validator should accept and leave splitting to the caller.
	var steps []psp.Step
	for i := 1; i <= 30; i++ {
		steps = append(steps, psp.Step{ID: fmt.Sprintf("step_%02d", i)})
	}
	if err := validateLogicalPlan(&psp.Plan{Steps: steps}); err != nil {
		t.Errorf("over-cap logical plan should pass: %v", err)
	}
}

func TestBuildFromSynthJSON_RejectsBadSynth(t *testing.T) {
	tmp := t.TempDir()
	if _, err := buildFromSynthJSON("garbage", "g", tmp, time.Now()); err == nil {
		t.Error("expected error for malformed synth JSON")
	}
}

// makeSynthJSON synthesises a synth output blob with N steps, each depending
// on its immediate predecessor — a strict chain. Used to drive builder
// tests where dep-resolution matters.
func makeSynthJSON(t *testing.T, goal string, n int) string {
	t.Helper()
	return synthJSONFixture(t, goal, n, true)
}

// makeSynthJSONNoDeps synthesises a synth output blob with N independent
// steps (no depends_on). Used by the split test to avoid the spec/impl gap
// around cross-sub-plan dep validation.
func makeSynthJSONNoDeps(t *testing.T, goal string, n int) string {
	t.Helper()
	return synthJSONFixture(t, goal, n, false)
}

func synthJSONFixture(t *testing.T, goal string, n int, chainDeps bool) string {
	t.Helper()
	var steps []map[string]interface{}
	for i := 1; i <= n; i++ {
		step := map[string]interface{}{
			"id":          fmt.Sprintf("step_%02d", i),
			"name":        fmt.Sprintf("Step %d", i),
			"description": fmt.Sprintf("Description for step %d", i),
			"expected_output": map[string]interface{}{
				"type":   "object",
				"fields": []string{"out"},
			},
		}
		if chainDeps && i > 1 {
			step["depends_on"] = []string{fmt.Sprintf("step_%02d", i-1)}
		}
		steps = append(steps, step)
	}
	doc := map[string]interface{}{
		"plan": map[string]interface{}{
			"goal":  goal,
			"steps": steps,
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal synth fixture: %v", err)
	}
	return string(body)
}
