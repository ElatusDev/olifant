package psp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RunnerConfig drives a single plan execution.
//
// Routing: when Executors is non-empty, each step is dispatched to
// Executors[step.ResolvedExecutor()]. When Executors is empty (or the
// step's executor is not registered), the runner falls back to Executor.
// This preserves backward compatibility with single-executor callers.
type RunnerConfig struct {
	Executor   Executor
	Executors  map[string]Executor // optional per-kind routing table (v1+)
	Plan       *Plan
	KBRoot     string // for short-term writes + signal resolution
	Verbose    bool
	TurnWriter func(record map[string]interface{}) error // optional hook for ledger writes
}

// pickExecutor resolves the executor for a step. Returns an error if the
// step requested a specific executor that is not registered.
func (cfg RunnerConfig) pickExecutor(step Step) (Executor, error) {
	if len(cfg.Executors) == 0 {
		if cfg.Executor == nil {
			return nil, fmt.Errorf("no executor configured")
		}
		return cfg.Executor, nil
	}
	kind := step.ResolvedExecutor()
	if e, ok := cfg.Executors[kind]; ok {
		return e, nil
	}
	// Step asked for a specific executor that's not registered.
	// If the step is using the default ("local") and cfg.Executor is set,
	// fall back. Otherwise it's a configuration error and we must abort
	// rather than silently routing to the wrong model.
	if step.Executor == "" && cfg.Executor != nil {
		return cfg.Executor, nil
	}
	return nil, fmt.Errorf("step %q requests executor %q which is not registered (available: %v)",
		step.ID, kind, executorKinds(cfg.Executors))
}

func executorKinds(m map[string]Executor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RunResult is what cmd/run.go surfaces after Run completes.
type RunResult struct {
	State        State
	Steps        []StepResult
	Aggregate    *Aggregate
	AggregatePath string
}

// Run executes a plan per PSP v1. State machine inlined for v0 — the
// transition log goes to stderr when Verbose; full ledger writes go via
// TurnWriter (caller-provided so cmd can wire short-term).
func Run(ctx context.Context, cfg RunnerConfig) (*RunResult, error) {
	if err := Validate(cfg.Plan); err != nil {
		return nil, fmt.Errorf("plan invalid: %w", err)
	}

	log := func(s State, note string) {
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  [psp:%s] %s\n", s, note)
		}
	}

	// 1. SYN handshake (in-process, but explicit for spec fidelity)
	log(StateListen, fmt.Sprintf("plan loaded — %d steps, goal=%q", len(cfg.Plan.Steps), cfg.Plan.Goal))
	log(StateSynSent, fmt.Sprintf("SYN dispatched to executors=%s", executorsSummary(cfg)))
	log(StateEstablished, "SYN_ACK received; session open")

	// Pre-flight: every executor a step asks for must be registered.
	if verr := validateExecutorRouting(cfg); verr != nil {
		return nil, verr
	}

	// 2. Topo-sort steps respecting depends_on
	ordered, err := topoSort(cfg.Plan.Steps)
	if err != nil {
		return nil, err
	}

	// 3. Initialise context state
	outputsByStepID := map[string]StepOutput{}
	if cfg.Plan.SeededFrom != "" {
		seed, sErr := loadSeed(cfg.KBRoot, cfg.Plan.SeededFrom)
		if sErr != nil && cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  [psp] warn: failed to load seed %s: %v\n", cfg.Plan.SeededFrom, sErr)
		}
		for k, v := range seed {
			outputsByStepID[k] = v
		}
	}

	stepResults := make([]StepResult, 0, len(ordered))
	cumulativeContextTokens := 0
	peakContextTokens := 0
	totalEvalTokens := 0
	totalAttempts := 0
	firstTryPasses := 0
	start := time.Now()

	// 4. Walk steps sequentially (v0 window = 1)
	for seq, step := range ordered {
		seq = seq + 1 // 1-indexed per spec
		log(StateTransmitting, fmt.Sprintf("STEP seq=%d step_id=%s name=%q", seq, step.ID, step.Name))

		stepResult, ferr := runStep(ctx, cfg, step, seq, outputsByStepID, cumulativeContextTokens, log)
		if ferr != nil {
			// Hard failure (executor unreachable or context cancelled) → RST
			log(StateClosedError, fmt.Sprintf("RST: hard error at seq=%d: %v", seq, ferr))
			stepResults = append(stepResults, stepResult)
			agg := makeAggregate(cfg.Plan, stepResults, "failure", StateClosedError, time.Since(start), totalEvalTokens, peakContextTokens, totalAttempts, firstTryPasses, outputsByStepID)
			path, _ := writeAggregate(cfg.KBRoot, agg)
			return &RunResult{State: StateClosedError, Steps: stepResults, Aggregate: agg, AggregatePath: path}, ferr
		}

		stepResults = append(stepResults, stepResult)
		totalEvalTokens += stepResult.EvalTokens
		totalAttempts += stepResult.Attempts
		cumulativeContextTokens = stepResult.ContextTokensConsumedSoFar
		if cumulativeContextTokens > peakContextTokens {
			peakContextTokens = cumulativeContextTokens
		}
		if stepResult.ValidationPassFirstTry {
			firstTryPasses++
		}

		if stepResult.State == StateClosedError {
			log(StateClosedError, fmt.Sprintf("RST: step %s exhausted retries", step.ID))
			agg := makeAggregate(cfg.Plan, stepResults, "failure", StateClosedError, time.Since(start), totalEvalTokens, peakContextTokens, totalAttempts, firstTryPasses, outputsByStepID)
			path, _ := writeAggregate(cfg.KBRoot, agg)
			return &RunResult{State: StateClosedError, Steps: stepResults, Aggregate: agg, AggregatePath: path}, nil
		}

		outputsByStepID[step.ID] = stepResult.Output
		log(StateEstablished, fmt.Sprintf("STEP_ACK seq=%d attempts=%d", seq, stepResult.Attempts))
	}

	// 5. FIN handshake
	log(StateFinWait, "FIN dispatched")
	log(StateClosedOK, "FIN_ACK received; plan complete")

	verdict := "success"
	if firstTryPasses < len(ordered) {
		verdict = "partial" // succeeded but with retries
	}
	agg := makeAggregate(cfg.Plan, stepResults, verdict, StateClosedOK, time.Since(start), totalEvalTokens, peakContextTokens, totalAttempts, firstTryPasses, outputsByStepID)
	path, werr := writeAggregate(cfg.KBRoot, agg)
	if werr != nil && cfg.Verbose {
		fmt.Fprintf(os.Stderr, "  [psp] warn: aggregate write failed: %v\n", werr)
	}
	return &RunResult{State: StateClosedOK, Steps: stepResults, Aggregate: agg, AggregatePath: path}, nil
}

// runStep is one step's inner loop: send → AWAITING_ACK → VALIDATING →
// STEP_ACK / RETRY / RST. Returns when the step terminally succeeds or
// terminally fails.
func runStep(
	ctx context.Context, cfg RunnerConfig, step Step, seq int,
	priorOutputs map[string]StepOutput, ctxTokensSoFar int,
	log func(s State, note string),
) (StepResult, error) {
	maxAttempts := step.RetryPolicy.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	backoffMs := step.RetryPolicy.BackoffMs
	if backoffMs <= 0 {
		backoffMs = 500
	}

	result := StepResult{
		Seq:        seq,
		StepID:     step.ID,
		StartedAt:  time.Now(),
	}

	var lastViolations []ValidationViolation
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempts = attempt
		log(StateAwaitingAck, fmt.Sprintf("attempt=%d", attempt))

		prompt := buildStepPrompt(step, priorOutputs, attempt, lastViolations)
		executor, perr := cfg.pickExecutor(step)
		if perr != nil {
			result.State = StateClosedError
			result.CompletedAt = time.Now()
			return result, fmt.Errorf("executor routing: %w", perr)
		}
		if attempt == 1 {
			log(StateAwaitingAck, fmt.Sprintf("routing seq=%d → executor=%s (%s)", seq, step.ResolvedExecutor(), executor.ID()))
		}
		execStart := time.Now()
		resp, eerr := executor.Execute(ctx, prompt, step.ExpectedOutput.Schema)
		execTimeMs := time.Since(execStart).Milliseconds()

		if eerr != nil {
			// Executor error — hard failure, no retry.
			result.State = StateClosedError
			result.CompletedAt = time.Now()
			result.ExecTimeMs = execTimeMs
			return result, fmt.Errorf("executor error: %w", eerr)
		}

		result.RawJSON = resp.RawText
		result.Output = resp.Output
		result.EvalTokens += resp.EvalTokens
		result.StepInputTokens += resp.PromptTokens
		result.StepOutputTokens += resp.OutputTokens
		result.CacheCreationTokens += resp.CacheCreationTokens
		result.CacheReadTokens += resp.CacheReadTokens
		result.ExecTimeMs += execTimeMs
		result.ExecutorKind = step.ResolvedExecutor()
		result.ExecutorID = executor.ID()

		log(StateValidating, fmt.Sprintf("seq=%d attempt=%d", seq, attempt))
		violations := validateStep(step, resp.Output)
		lastViolations = violations

		if !hasBlocker(violations) {
			result.State = StateClosedOK
			result.ValidationPassFirstTry = (attempt == 1)
			result.FinalViolations = violations
			result.ContextTokensConsumedSoFar = ctxTokensSoFar + result.StepInputTokens + result.StepOutputTokens
			result.CompletedAt = time.Now()
			return result, nil
		}

		log(StateRetry, fmt.Sprintf("seq=%d attempt=%d blockers=%d", seq, attempt, countBlockers(violations)))
		if attempt < maxAttempts {
			// Exponential backoff
			d := time.Duration(backoffMs) * time.Millisecond * (1 << (attempt - 1))
			if d > 30*time.Second {
				d = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				result.State = StateClosedError
				result.CompletedAt = time.Now()
				return result, ctx.Err()
			case <-time.After(d):
			}
		}
	}

	// Exhausted attempts → terminal NAK → RST
	result.State = StateClosedError
	result.FinalViolations = lastViolations
	result.CompletedAt = time.Now()
	result.ContextTokensConsumedSoFar = ctxTokensSoFar + result.StepInputTokens + result.StepOutputTokens
	return result, nil
}

// buildStepPrompt composes the prompt for one step segment. Prior outputs
// are inlined; signals are listed by path (NOT inlined as text — Claude
// would read them; for v0 local executor we list them so the prompt is
// faithful to the protocol even though the local model can't actually
// open files).
func buildStepPrompt(step Step, priorOutputs map[string]StepOutput, attempt int, lastViolations []ValidationViolation) string {
	var sb strings.Builder
	sb.WriteString("# Prompt-step segment\n\n")
	if step.Name != "" {
		fmt.Fprintf(&sb, "Name: %s\n", step.Name)
	}
	fmt.Fprintf(&sb, "Step ID: %s\n", step.ID)
	fmt.Fprintf(&sb, "Attempt: %d\n\n", attempt)

	fmt.Fprintf(&sb, "## Description\n\n%s\n\n", step.Description)

	if len(step.Signals) > 0 {
		sb.WriteString("## Signals (files to read)\n\n")
		for _, s := range step.Signals {
			fmt.Fprintf(&sb, "  - %s\n", s)
		}
		sb.WriteString("\n")
	}

	if len(step.DependsOn) > 0 {
		sb.WriteString("## Prior step outputs\n\n")
		for _, dep := range step.DependsOn {
			out, ok := priorOutputs[dep]
			if !ok {
				fmt.Fprintf(&sb, "  - %s: (missing)\n", dep)
				continue
			}
			j, _ := json.Marshal(out)
			fmt.Fprintf(&sb, "  - %s: %s\n", dep, string(j))
		}
		sb.WriteString("\n")
	}

	if len(lastViolations) > 0 {
		sb.WriteString("## Previous attempt had validation violations — fix these:\n\n")
		for _, v := range lastViolations {
			fmt.Fprintf(&sb, "  - [%s] %s @ %s", v.Severity, v.Code, v.Location)
			if v.Value != "" {
				fmt.Fprintf(&sb, "  (%q)", v.Value)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Produce JSON conforming to the schema. Output only the JSON.\n")
	return sb.String()
}

// validateStep — v0 minimal: confirm response is non-nil + JSON-parseable.
// Per-rule named validators (validation_rules field) deferred to v0.1.
func validateStep(step Step, output StepOutput) []ValidationViolation {
	var out []ValidationViolation
	if output == nil {
		out = append(out, ValidationViolation{
			Severity: "BLOCKER", Code: "no_output",
			Location: "(root)", Note: "executor returned no parseable output",
		})
		return out
	}
	// Future: walk step.ValidationRules and look up in a registry.
	return out
}

func hasBlocker(vs []ValidationViolation) bool {
	for _, v := range vs {
		if v.Severity == "BLOCKER" {
			return true
		}
	}
	return false
}

func countBlockers(vs []ValidationViolation) int {
	n := 0
	for _, v := range vs {
		if v.Severity == "BLOCKER" {
			n++
		}
	}
	return n
}

// topoSort orders steps by depends_on (Kahn's algorithm).
func topoSort(steps []Step) ([]Step, error) {
	byID := map[string]Step{}
	indeg := map[string]int{}
	deps := map[string][]string{}
	for _, s := range steps {
		byID[s.ID] = s
		indeg[s.ID] = len(s.DependsOn)
		deps[s.ID] = s.DependsOn
	}
	// Validate referenced deps exist
	for id, ds := range deps {
		for _, d := range ds {
			if _, ok := byID[d]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", id, d)
			}
		}
	}
	var queue []string
	// Process in plan order for determinism
	for _, s := range steps {
		if indeg[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}
	var ordered []Step
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		ordered = append(ordered, byID[head])
		// Find dependents of head
		for _, s := range steps {
			for _, d := range s.DependsOn {
				if d == head {
					indeg[s.ID]--
					if indeg[s.ID] == 0 {
						queue = append(queue, s.ID)
					}
				}
			}
		}
	}
	if len(ordered) != len(steps) {
		return nil, fmt.Errorf("topo sort failed — dependency cycle?")
	}
	return ordered, nil
}

// executorsSummary renders the registered executors for handshake logging.
// Falls back to the single Executor when Executors is empty.
func executorsSummary(cfg RunnerConfig) string {
	if len(cfg.Executors) > 0 {
		parts := make([]string, 0, len(cfg.Executors))
		for _, k := range executorKinds(cfg.Executors) {
			parts = append(parts, fmt.Sprintf("%s=%s", k, cfg.Executors[k].ID()))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if cfg.Executor != nil {
		return cfg.Executor.ID()
	}
	return "(none)"
}

// validateExecutorRouting confirms every step's requested executor is
// resolvable before the plan starts. Without this, a misrouted step would
// fail mid-execution after spending tokens on prior steps.
func validateExecutorRouting(cfg RunnerConfig) error {
	for _, step := range cfg.Plan.Steps {
		if _, err := cfg.pickExecutor(step); err != nil {
			return err
		}
	}
	return nil
}

// Validate runs static checks on a plan before execution. Per psp-v1.md §6.
func Validate(p *Plan) error {
	if p == nil {
		return fmt.Errorf("plan is nil")
	}
	if p.PlanID == "" {
		return fmt.Errorf("plan_id is required")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	if len(p.Steps) > MaxStepsPerPlan {
		return fmt.Errorf("plan has %d steps; cap is %d (mps_exceeded — split this plan into sub-plans)",
			len(p.Steps), MaxStepsPerPlan)
	}
	// IDs must be unique
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.ID == "" {
			return fmt.Errorf("step has empty id")
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate step id: %s", s.ID)
		}
		seen[s.ID] = true
	}
	// Verify topo-sort succeeds (detects unknown deps + cycles)
	if _, err := topoSort(p.Steps); err != nil {
		return err
	}
	return nil
}

// LoadPlan reads a plan YAML file from disk.
func LoadPlan(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// makeAggregate builds the per-plan summary from collected step results.
func makeAggregate(
	plan *Plan, steps []StepResult, verdict string, state State,
	elapsed time.Duration, totalEvalTokens int, peakContextTokens int,
	totalAttempts int, firstTryPasses int,
	finalOutputs map[string]StepOutput,
) *Aggregate {
	agg := &Aggregate{
		PlanID:               plan.PlanID,
		SessionID:            plan.SessionID,
		Goal:                 plan.Goal,
		State:                state,
		TotalSteps:           len(plan.Steps),
		TotalAttempts:        totalAttempts,
		TotalElapsedMs:       elapsed.Milliseconds(),
		TotalEvalTokens:      totalEvalTokens,
		PeakContextTokens:    peakContextTokens,
		Verdict:              verdict,
		FinalOutputsByStepID: finalOutputs,
	}
	if len(steps) > 0 {
		agg.FirstTryPassRate = float64(firstTryPasses) / float64(len(steps))
	}
	for _, sr := range steps {
		agg.StepSummaries = append(agg.StepSummaries, StepSummary{
			Seq:                 sr.Seq,
			StepID:              sr.StepID,
			State:               sr.State,
			Attempts:            sr.Attempts,
			ElapsedMs:           sr.ExecTimeMs,
			EvalTokens:          sr.EvalTokens,
			ExecutorKind:        sr.ExecutorKind,
			ExecutorID:          sr.ExecutorID,
			CacheCreationTokens: sr.CacheCreationTokens,
			CacheReadTokens:     sr.CacheReadTokens,
		})
	}
	return agg
}

// writeAggregate persists the per-plan summary to short-term/plans/<plan_id>/aggregate.yaml.
func writeAggregate(kbRoot string, agg *Aggregate) (string, error) {
	if kbRoot == "" || agg == nil {
		return "", nil
	}
	dir := filepath.Join(kbRoot, "short-term", "plans", agg.PlanID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	abs := filepath.Join(dir, "aggregate.yaml")
	header := fmt.Sprintf("# Olifant PSP plan aggregate.\n# Schema: dsl/psp-v1.md §9\n# Written: %s\n\n",
		time.Now().UTC().Format(time.RFC3339))
	body, err := yaml.Marshal(agg)
	if err != nil {
		return "", err
	}
	return abs, os.WriteFile(abs, []byte(header+string(body)), 0o644)
}

// loadSeed reads a previous sub-plan's aggregate.yaml and returns its
// final_outputs_by_step_id for use as this plan's prior_outputs seed.
func loadSeed(kbRoot, seedRef string) (map[string]StepOutput, error) {
	// seedRef is typically: 2026-05-14T20-15-00Z-abc123.part-1-of-2/aggregate.yaml
	path := filepath.Join(kbRoot, "short-term", "plans", seedRef)
	if !strings.HasSuffix(seedRef, ".yaml") {
		path = filepath.Join(kbRoot, "short-term", "plans", seedRef, "aggregate.yaml")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var agg Aggregate
	if err := yaml.Unmarshal(raw, &agg); err != nil {
		return nil, err
	}
	if agg.FinalOutputsByStepID == nil {
		return map[string]StepOutput{}, nil
	}
	return agg.FinalOutputsByStepID, nil
}

// Split partitions a logical step list (potentially > MaxStepsPerPlan) into
// a sequential chain of sub-plans, each ≤ MaxStepsPerPlan. Per psp-v1.md §7.
// Returns nil if no split is needed (i.e., steps fit in one plan).
func Split(plan *Plan) []*Plan {
	if len(plan.Steps) <= MaxStepsPerPlan {
		return nil
	}
	ordered, err := topoSort(plan.Steps)
	if err != nil {
		// Caller should have validated already; just return one over-sized plan and let validate fail.
		return nil
	}
	sessionID := plan.PlanID
	var parts []*Plan
	idx := 0
	partN := 1
	for idx < len(ordered) {
		end := idx + MaxStepsPerPlan
		if end > len(ordered) {
			end = len(ordered)
		}
		// Total parts is provisional — recomputed after loop
		sub := &Plan{
			PlanID:    fmt.Sprintf("%s.part-%d-of-?", plan.PlanID, partN),
			SessionID: sessionID,
			Goal:      plan.Goal,
			Scope:     plan.Scope,
			CreatedAt: plan.CreatedAt,
			CreatedBy: plan.CreatedBy + " (split)",
			Steps:     append([]Step(nil), ordered[idx:end]...),
		}
		if partN > 1 {
			sub.SeededFrom = parts[partN-2].PlanID
		}
		parts = append(parts, sub)
		idx = end
		partN++
	}
	// Patch the total in each plan id (now we know M)
	M := len(parts)
	for i, p := range parts {
		p.PlanID = fmt.Sprintf("%s.part-%d-of-%d", plan.PlanID, i+1, M)
		if i > 0 {
			p.SeededFrom = parts[i-1].PlanID
		}
	}
	// Sort step summaries by ID (deterministic test friendly)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].PlanID < parts[j].PlanID })
	return parts
}
