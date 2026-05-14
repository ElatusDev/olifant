// Package psp implements the Prompt-Step Protocol v1 (knowledge-base/
// dsl/psp-v1.md). Olifant's runner walks a plan, sends each step to the
// executor, validates per-step output, retries on NAK, records every
// transition to the short-term ledger.
package psp

import "time"

// MaxStepsPerPlan is the v1 cap (provisional, tuned via olifant plan stats).
// Plans with len(Steps) > this are split via Split() into sub-plans.
const MaxStepsPerPlan = 25

// State is the PSP plan-execution state.
type State string

const (
	StateClosed       State = "CLOSED"
	StateListen       State = "LISTEN"
	StateSynSent      State = "SYN_SENT"
	StateEstablished  State = "ESTABLISHED"
	StateTransmitting State = "TRANSMITTING"
	StateAwaitingAck  State = "AWAITING_ACK"
	StateValidating   State = "VALIDATING"
	StateRetry        State = "RETRY"
	StateFinWait      State = "FIN_WAIT"
	StateClosedOK     State = "CLOSED_OK"
	StateClosedError  State = "CLOSED_ERROR"
)

// SegmentType — names from psp-v1.md §3.
type SegmentType string

const (
	SegSYN     SegmentType = "SYN"
	SegSYNACK  SegmentType = "SYN_ACK"
	SegACK     SegmentType = "ACK"
	SegSTEP    SegmentType = "STEP"
	SegSTEPACK SegmentType = "STEP_ACK"
	SegSTEPNAK SegmentType = "STEP_NAK"
	SegFIN     SegmentType = "FIN"
	SegFINACK  SegmentType = "FIN_ACK"
	SegRST     SegmentType = "RST"
)

// Plan is the on-disk shape of plans/<plan_id>.yaml.
type Plan struct {
	PlanID    string   `yaml:"plan_id"`
	SessionID string   `yaml:"session_id,omitempty"` // populated if part of a split chain
	Goal      string   `yaml:"goal"`
	Scope     []string `yaml:"scope,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
	CreatedBy string   `yaml:"created_by,omitempty"`
	Steps     []Step   `yaml:"steps"`

	// SeededFrom — for sub-plans in a split chain. Points to a previous
	// sub-plan's aggregate.yaml whose final outputs seed this plan's
	// prior_outputs map.
	SeededFrom string `yaml:"seeded_from,omitempty"`
}

// Step is one prompt-step in a plan.
type Step struct {
	ID             string                 `yaml:"id"`
	Name           string                 `yaml:"name,omitempty"`
	Description    string                 `yaml:"description"`
	Signals        []string               `yaml:"signals,omitempty"`
	ExpectedOutput ExpectedOutput         `yaml:"expected_output"`
	ValidationRules []string              `yaml:"validation_rules,omitempty"`
	DependsOn      []string               `yaml:"depends_on,omitempty"`
	RetryPolicy    RetryPolicy            `yaml:"retry_policy,omitempty"`
	BudgetMs       int                    `yaml:"budget_ms,omitempty"`
}

// ExpectedOutput defines the per-step contract.
type ExpectedOutput struct {
	// Schema is a JSON Schema object passed to the executor's format
	// constraint (when supported by the executor model).
	Schema map[string]interface{} `yaml:"schema"`
}

// RetryPolicy controls per-step retry behavior.
type RetryPolicy struct {
	MaxAttempts int `yaml:"max_attempts,omitempty"`
	BackoffMs   int `yaml:"backoff_ms,omitempty"`
}

// StepOutput is the executor's structured response for one step. Stored
// as a generic map so per-step schemas vary.
type StepOutput map[string]interface{}

// StepResult is the per-step turn record produced by the runner.
type StepResult struct {
	Seq                          int
	StepID                       string
	Attempts                     int
	State                        State // STEP_ACK or STEP_NAK or RST
	Output                       StepOutput
	RawJSON                      string
	ExecTimeMs                   int64
	EvalTokens                   int
	StepInputTokens              int // estimated
	StepOutputTokens             int
	ContextTokensConsumedSoFar   int // cumulative
	ValidationPassFirstTry       bool
	FinalViolations              []ValidationViolation
	StartedAt                    time.Time
	CompletedAt                  time.Time
}

// ValidationViolation is the per-step validator's BLOCKER/WARNING/INFO.
type ValidationViolation struct {
	Severity string `yaml:"severity"`
	Code     string `yaml:"code"`
	Location string `yaml:"location,omitempty"`
	Value    string `yaml:"value,omitempty"`
	Note     string `yaml:"note,omitempty"`
}

// Aggregate is the per-plan summary written to short-term/plans/<plan_id>/aggregate.yaml.
type Aggregate struct {
	PlanID                string        `yaml:"plan_id"`
	SessionID             string        `yaml:"session_id,omitempty"`
	Goal                  string        `yaml:"goal"`
	State                 State         `yaml:"state"`
	TotalSteps            int           `yaml:"total_steps"`
	TotalAttempts         int           `yaml:"total_attempts"`
	TotalElapsedMs        int64         `yaml:"total_elapsed_ms"`
	TotalEvalTokens       int           `yaml:"total_eval_tokens"`
	PeakContextTokens     int           `yaml:"peak_context_tokens"`
	FirstTryPassRate      float64       `yaml:"first_try_pass_rate"`
	Verdict               string        `yaml:"verdict"` // success | partial | failure
	StepSummaries         []StepSummary `yaml:"step_summaries"`
	FinalOutputsByStepID  map[string]StepOutput `yaml:"final_outputs_by_step_id,omitempty"`
}

// StepSummary is one row in Aggregate.StepSummaries.
type StepSummary struct {
	Seq        int    `yaml:"seq"`
	StepID     string `yaml:"step_id"`
	State      State  `yaml:"state"`
	Attempts   int    `yaml:"attempts"`
	ElapsedMs  int64  `yaml:"elapsed_ms"`
	EvalTokens int    `yaml:"eval_tokens,omitempty"`
}
