package psp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkSteps(n int) []Step {
	steps := make([]Step, n)
	for i := 0; i < n; i++ {
		steps[i] = Step{ID: stepID(i + 1), Description: "do work"}
		if i > 0 {
			steps[i].DependsOn = []string{stepID(i)}
		}
	}
	return steps
}

func stepID(n int) string {
	if n < 10 {
		return "step_0" + string(rune('0'+n))
	}
	return "step_1" + string(rune('0'+n-10))
}

func TestValidate(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("nil plan should error")
	}
	if err := Validate(&Plan{}); err == nil {
		t.Error("missing plan_id should error")
	}
	if err := Validate(&Plan{PlanID: "p"}); err == nil {
		t.Error("no steps should error")
	}
	// Over cap.
	if err := Validate(&Plan{PlanID: "p", Steps: mkSteps(MaxStepsPerPlan + 1)}); err == nil ||
		!strings.Contains(err.Error(), "mps_exceeded") {
		t.Errorf("over-cap plan should error mps_exceeded, got %v", err)
	}
	// Empty step id.
	if err := Validate(&Plan{PlanID: "p", Steps: []Step{{ID: ""}}}); err == nil {
		t.Error("empty step id should error")
	}
	// Duplicate id.
	dup := &Plan{PlanID: "p", Steps: []Step{{ID: "step_01"}, {ID: "step_01"}}}
	if err := Validate(dup); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate id should error, got %v", err)
	}
	// Unknown dep.
	bad := &Plan{PlanID: "p", Steps: []Step{{ID: "step_01", DependsOn: []string{"step_99"}}}}
	if err := Validate(bad); err == nil {
		t.Error("unknown dep should error")
	}
	// Valid.
	if err := Validate(&Plan{PlanID: "p", Steps: mkSteps(3)}); err != nil {
		t.Errorf("valid plan errored: %v", err)
	}
}

func TestTopoSort(t *testing.T) {
	ordered, err := topoSort(mkSteps(3))
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	if ordered[0].ID != "step_01" || ordered[2].ID != "step_03" {
		t.Errorf("order = %v", []string{ordered[0].ID, ordered[1].ID, ordered[2].ID})
	}

	// Cycle: a→b, b→a.
	cyclic := []Step{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}}
	if _, err := topoSort(cyclic); err == nil {
		t.Error("cycle should error")
	}

	// Unknown dep.
	if _, err := topoSort([]Step{{ID: "a", DependsOn: []string{"ghost"}}}); err == nil {
		t.Error("unknown dep should error")
	}
}

func TestLoadPlan(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plan.yaml")
	_ = os.WriteFile(p, []byte("plan_id: p1\ngoal: g\nsteps:\n  - id: step_01\n    description: d\n"), 0o644)
	plan, err := LoadPlan(p)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if plan.PlanID != "p1" || len(plan.Steps) != 1 {
		t.Errorf("plan = %+v", plan)
	}

	if _, err := LoadPlan(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file should error")
	}

	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("plan_id: [unterminated"), 0o644)
	if _, err := LoadPlan(bad); err == nil {
		t.Error("invalid yaml should error")
	}
}

func TestValidateStep_And_Blockers(t *testing.T) {
	// nil output → BLOCKER.
	vs := validateStep(Step{ID: "step_01"}, nil)
	if !hasBlocker(vs) || countBlockers(vs) != 1 {
		t.Errorf("nil output should yield 1 blocker, got %v", vs)
	}
	// valid output → no violations.
	vs2 := validateStep(Step{ID: "step_01"}, StepOutput{"k": "v"})
	if hasBlocker(vs2) || countBlockers(vs2) != 0 {
		t.Errorf("valid output should yield 0 blockers, got %v", vs2)
	}
	// mixed severities.
	mixed := []ValidationViolation{{Severity: "BLOCKER"}, {Severity: "WARNING"}, {Severity: "BLOCKER"}}
	if countBlockers(mixed) != 2 {
		t.Errorf("countBlockers mixed = %d, want 2", countBlockers(mixed))
	}
	if hasBlocker([]ValidationViolation{{Severity: "WARNING"}}) {
		t.Error("warning-only should not report blocker")
	}
}

func TestBuildStepPrompt(t *testing.T) {
	step := Step{
		ID: "step_02", Name: "Design", Description: "design the key",
		Signals: []string{"patterns/backend.md"}, DependsOn: []string{"step_01"},
	}
	prior := map[string]StepOutput{"step_01": {"summary": "did survey"}}
	viols := []ValidationViolation{{Severity: "BLOCKER", Code: "no_output", Location: "(root)", Value: "x"}}

	out := buildStepPrompt(step, prior, 2, viols)
	for _, want := range []string{"Step ID: step_02", "Attempt: 2", "design the key", "patterns/backend.md", "did survey", "no_output"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}

	// Missing prior output is annotated.
	out2 := buildStepPrompt(Step{ID: "step_02", DependsOn: []string{"step_01"}}, nil, 1, nil)
	if !strings.Contains(out2, "(missing)") {
		t.Errorf("missing dep not annotated:\n%s", out2)
	}
}

func TestWriteAggregate(t *testing.T) {
	// Empty kbRoot → no-op, no error.
	if path, err := writeAggregate("", &Aggregate{PlanID: "p"}); err != nil || path != "" {
		t.Errorf("empty kbRoot = (%q,%v), want (\"\",nil)", path, err)
	}

	kb := t.TempDir()
	abs, err := writeAggregate(kb, &Aggregate{PlanID: "plan-1", Goal: "g", Verdict: "success"})
	if err != nil {
		t.Fatalf("writeAggregate: %v", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read aggregate: %v", err)
	}
	if !strings.Contains(string(raw), "plan_id: plan-1") || !strings.HasPrefix(string(raw), "# Olifant PSP plan aggregate") {
		t.Errorf("aggregate content unexpected:\n%s", raw)
	}
}

func TestLoadSeed(t *testing.T) {
	kb := t.TempDir()
	// Write an aggregate that loadSeed will read back.
	abs, err := writeAggregate(kb, &Aggregate{
		PlanID:               "plan-1.part-1-of-2",
		FinalOutputsByStepID: map[string]StepOutput{"step_01": {"k": "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = abs

	// seedRef without .yaml suffix → loadSeed appends aggregate.yaml.
	out, err := loadSeed(kb, "plan-1.part-1-of-2")
	if err != nil {
		t.Fatalf("loadSeed dir form: %v", err)
	}
	if out["step_01"]["k"] != "v" {
		t.Errorf("seed outputs = %v", out)
	}

	// seedRef with explicit .yaml suffix.
	out2, err := loadSeed(kb, filepath.Join("plan-1.part-1-of-2", "aggregate.yaml"))
	if err != nil {
		t.Fatalf("loadSeed file form: %v", err)
	}
	if out2["step_01"]["k"] != "v" {
		t.Errorf("seed (file form) = %v", out2)
	}

	if _, err := loadSeed(kb, "missing-plan"); err == nil {
		t.Error("missing seed should error")
	}
}

func TestSplit(t *testing.T) {
	// Under cap → nil (no split).
	if parts := Split(&Plan{PlanID: "p", Steps: mkSteps(MaxStepsPerPlan)}); parts != nil {
		t.Errorf("under-cap split = %d parts, want nil", len(parts))
	}

	// Over cap → chained sub-plans, each ≤ cap.
	plan := &Plan{PlanID: "2026-05-14T20-15-00Z-abc123", Goal: "g", Steps: mkSteps(MaxStepsPerPlan + 5)}
	parts := Split(plan)
	if len(parts) != 2 {
		t.Fatalf("split into %d parts, want 2", len(parts))
	}
	if len(parts[0].Steps) != MaxStepsPerPlan || len(parts[1].Steps) != 5 {
		t.Errorf("part sizes = %d/%d, want %d/5", len(parts[0].Steps), len(parts[1].Steps), MaxStepsPerPlan)
	}
	if !strings.HasSuffix(parts[0].PlanID, ".part-1-of-2") {
		t.Errorf("part-1 id = %q", parts[0].PlanID)
	}
	if parts[1].SeededFrom != parts[0].PlanID {
		t.Errorf("part-2 SeededFrom = %q, want %q", parts[1].SeededFrom, parts[0].PlanID)
	}
}
