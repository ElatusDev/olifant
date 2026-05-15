package psp

import (
	"context"
	"strings"
	"testing"
)

// stubExecutor records every Execute() call so tests can assert routing.
type stubExecutor struct {
	id    string
	calls int
	last  string
}

func (s *stubExecutor) ID() string { return s.id }

func (s *stubExecutor) Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error) {
	s.calls++
	s.last = prompt
	// Return a minimal but schema-conforming response.
	return Response{
		RawText: `{"ok":true}`,
		Output:  StepOutput{"ok": true},
	}, nil
}

func minimalPlan(steps []Step) *Plan {
	return &Plan{
		PlanID: "test-plan",
		Goal:   "routing test",
		Steps:  steps,
	}
}

func minimalStep(id, executor string) Step {
	return Step{
		ID:          id,
		Description: "test step " + id,
		ExpectedOutput: ExpectedOutput{
			Schema: map[string]interface{}{"type": "object"},
		},
		Executor: executor,
	}
}

func TestStep_ResolvedExecutor_EmptyDefaultsToLocal(t *testing.T) {
	if got := (Step{}).ResolvedExecutor(); got != ExecutorKindLocal {
		t.Errorf("empty Executor should resolve to %q, got %q", ExecutorKindLocal, got)
	}
	if got := (Step{Executor: "claude"}).ResolvedExecutor(); got != "claude" {
		t.Errorf("explicit claude should resolve to %q, got %q", "claude", got)
	}
}

func TestPickExecutor_SingleExecutorBackwardCompat(t *testing.T) {
	local := &stubExecutor{id: "qwen"}
	cfg := RunnerConfig{Executor: local}
	e, err := cfg.pickExecutor(Step{})
	if err != nil {
		t.Fatalf("pickExecutor: %v", err)
	}
	if e != local {
		t.Errorf("expected local executor, got %v", e)
	}
}

func TestPickExecutor_RoutingTable(t *testing.T) {
	local := &stubExecutor{id: "qwen"}
	claude := &stubExecutor{id: "sonnet"}
	cfg := RunnerConfig{
		Executors: map[string]Executor{
			"local":  local,
			"claude": claude,
		},
	}

	e, err := cfg.pickExecutor(Step{Executor: "local"})
	if err != nil || e != local {
		t.Errorf("local routing: e=%v err=%v", e, err)
	}
	e, err = cfg.pickExecutor(Step{Executor: "claude"})
	if err != nil || e != claude {
		t.Errorf("claude routing: e=%v err=%v", e, err)
	}
	// Empty Executor field → resolves to "local"
	e, err = cfg.pickExecutor(Step{})
	if err != nil || e != local {
		t.Errorf("default routing: e=%v err=%v", e, err)
	}
}

func TestPickExecutor_UnregisteredKindReturnsError(t *testing.T) {
	local := &stubExecutor{id: "qwen"}
	cfg := RunnerConfig{
		Executors: map[string]Executor{"local": local},
	}
	_, err := cfg.pickExecutor(Step{Executor: "claude"})
	if err == nil {
		t.Fatal("expected error for unregistered executor kind")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should mention the requested kind: %v", err)
	}
}

func TestPickExecutor_DefaultFallsBackToCfgExecutor(t *testing.T) {
	// When Executors is provided but doesn't include "local", a step with no
	// Executor field still works because cfg.Executor is the fallback.
	local := &stubExecutor{id: "qwen"}
	specialOnly := &stubExecutor{id: "special"}
	cfg := RunnerConfig{
		Executor:  local,
		Executors: map[string]Executor{"special": specialOnly},
	}
	e, err := cfg.pickExecutor(Step{})
	if err != nil {
		t.Fatalf("pickExecutor: %v", err)
	}
	if e != local {
		t.Errorf("expected fallback to cfg.Executor, got %v", e)
	}
}

func TestRun_RoutesMixedExecutors(t *testing.T) {
	local := &stubExecutor{id: "qwen"}
	claude := &stubExecutor{id: "sonnet"}
	plan := minimalPlan([]Step{
		minimalStep("s1", "local"),
		minimalStep("s2", "claude"),
		minimalStep("s3", ""), // empty → local
	})
	cfg := RunnerConfig{
		Executor: local, // backward-compat
		Executors: map[string]Executor{
			ExecutorKindLocal:  local,
			ExecutorKindClaude: claude,
		},
		Plan: plan,
	}

	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.State != StateClosedOK {
		t.Errorf("State=%v want CLOSED_OK", result.State)
	}
	if local.calls != 2 {
		t.Errorf("local.calls=%d want 2 (s1 + s3)", local.calls)
	}
	if claude.calls != 1 {
		t.Errorf("claude.calls=%d want 1 (s2)", claude.calls)
	}

	// Verify executor identity surfaced on each StepResult.
	if result.Steps[0].ExecutorKind != "local" {
		t.Errorf("s1 ExecutorKind=%q want local", result.Steps[0].ExecutorKind)
	}
	if result.Steps[1].ExecutorKind != "claude" {
		t.Errorf("s2 ExecutorKind=%q want claude", result.Steps[1].ExecutorKind)
	}
	if result.Steps[1].ExecutorID != "sonnet" {
		t.Errorf("s2 ExecutorID=%q want sonnet", result.Steps[1].ExecutorID)
	}
}

func TestRun_PreflightRejectsUnknownExecutor(t *testing.T) {
	local := &stubExecutor{id: "qwen"}
	plan := minimalPlan([]Step{
		minimalStep("s1", "local"),
		minimalStep("s2", "claude"), // not registered
	})
	cfg := RunnerConfig{
		Executors: map[string]Executor{ExecutorKindLocal: local},
		Plan:      plan,
	}
	_, err := Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected pre-flight error for unregistered claude executor")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should mention 'claude': %v", err)
	}
	// Crucial: local executor should not be invoked at all when pre-flight fails.
	if local.calls != 0 {
		t.Errorf("local.calls=%d; pre-flight should reject before any execution", local.calls)
	}
}

func TestExecutorsSummary(t *testing.T) {
	// Single-executor path
	if s := executorsSummary(RunnerConfig{Executor: &stubExecutor{id: "qwen"}}); s != "qwen" {
		t.Errorf("single: got %q want qwen", s)
	}
	// Multi-executor path — deterministic ordering matters for log readability.
	cfg := RunnerConfig{
		Executors: map[string]Executor{
			"claude": &stubExecutor{id: "sonnet"},
			"local":  &stubExecutor{id: "qwen"},
		},
	}
	s := executorsSummary(cfg)
	if !strings.Contains(s, "local=qwen") || !strings.Contains(s, "claude=sonnet") {
		t.Errorf("multi: got %q missing kind=id entries", s)
	}
	// Verify alphabetical order: claude before local
	if strings.Index(s, "claude=") > strings.Index(s, "local=") {
		t.Errorf("expected alphabetical order, got %q", s)
	}
}
