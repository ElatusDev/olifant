// Package eval implements `olifant eval run <suite.yaml>` — the Go-native
// replacement for the ad-hoc bash battery scripts.
//
// Per platform-process constraint `tooling-in-go`, all olifant tooling
// (including eval harnesses) is Go. Suites are YAML; runs persist to the
// short-term ledger.
package eval

import "time"

// Suite is the static definition of an eval battery.
type Suite struct {
	SuiteID     string       `yaml:"suite_id"`
	Description string       `yaml:"description,omitempty"`
	Created     string       `yaml:"created,omitempty"`
	Default     CaseDefaults `yaml:"default,omitempty"`
	Cases       []Case       `yaml:"cases"`
}

// CaseDefaults supplies fall-back values for each case in the suite.
type CaseDefaults struct {
	TopN       int    `yaml:"top_n,omitempty"`
	MaxTokens  int    `yaml:"max_tokens,omitempty"`
	TimeoutSec int    `yaml:"timeout_sec,omitempty"`
	Synth      string `yaml:"synth,omitempty"`
}

// Case is one evaluation row.
type Case struct {
	ID          string          `yaml:"id"`
	Scope       []string        `yaml:"scope,omitempty"`
	File        string          `yaml:"file,omitempty"`         // relative to platform root; passed as --file
	Request     string          `yaml:"request,omitempty"`      // alternative to File: a literal request string
	Claim       string          `yaml:"claim,omitempty"`        // validate surface (olifant#86): full claim text
	Diff        string          `yaml:"diff,omitempty"`         // validate surface: frozen diff snapshot (self-contained, D-VC3)
	TopN        int             `yaml:"top_n,omitempty"`        // overrides Default.TopN
	MaxTokens   int             `yaml:"max_tokens,omitempty"`   // overrides Default.MaxTokens
	TimeoutSec  int             `yaml:"timeout_sec,omitempty"`  // overrides Default.TimeoutSec
	Synth       string          `yaml:"synth,omitempty"`        // overrides Default.Synth
	Expected    *Expected       `yaml:"expected,omitempty"`     // optional graded eval
	FileContent string          `yaml:"file_content,omitempty"` // advice surface (#110): inline code snippet
	Advice      *AdviceExpected `yaml:"advice,omitempty"`       // advice-quality expectations (per-bucket cites)
}

// IsValidate reports whether a case exercises the validate surface (claim +
// frozen diff) rather than challenge (file/request). olifant#86.
func (c Case) IsValidate() bool { return c.Claim != "" && c.Diff != "" }

// IsAdvice reports whether a case exercises the T1 retrieval-advice surface
// (inline code + per-bucket cite expectations) rather than a verdict surface.
// Retrieval-only, measurement — never gates to blocking (D269). olifant#110.
func (c Case) IsAdvice() bool { return c.FileContent != "" && c.Advice != nil }

// AdviceExpected is the per-bucket cite contract for a retrieve --file case: the
// cite ids that must surface in each avoid/prefer/convention bucket (#110).
type AdviceExpected struct {
	ExpectAvoid      []string `yaml:"expect_avoid,omitempty"`
	ExpectPrefer     []string `yaml:"expect_prefer,omitempty"`
	ExpectConvention []string `yaml:"expect_convention,omitempty"`
}

// Expected is the optional graded-eval contract for a case.
type Expected struct {
	Verdict          string   `yaml:"verdict,omitempty"`          // exact match against output verdict
	MaxBlockers      *int     `yaml:"max_blockers,omitempty"`     // pointer so 0 is meaningful
	MustCiteAnyOf    []string `yaml:"must_cite_any_of,omitempty"` // at least one of these in cites
	MustNotCiteAnyOf []string `yaml:"must_not_cite_any_of,omitempty"`
}

// CaseResult captures one case's outcome.
type CaseResult struct {
	CaseID         string         `yaml:"case_id"`
	Scope          []string       `yaml:"scope,omitempty"`
	File           string         `yaml:"file,omitempty"`
	Verdict        string         `yaml:"verdict,omitempty"`
	Proceed        string         `yaml:"proceed,omitempty"`
	Blockers       int            `yaml:"blockers"`
	Warnings       int            `yaml:"warnings"`
	Infos          int            `yaml:"infos"`
	Attempts       int            `yaml:"attempts"`
	RetrievedCount int            `yaml:"retrieved_count"`
	ElapsedMs      int64          `yaml:"elapsed_ms"`
	EmbedMs        int64          `yaml:"embed_ms"`
	RetrieveMs     int64          `yaml:"retrieve_ms"`
	SynthMs        int64          `yaml:"synth_ms"`
	EvalTokens     int            `yaml:"eval_tokens"`
	TokensPerSec   float64        `yaml:"tokens_per_sec"`
	OutputYAMLPath string         `yaml:"output_yaml_path"`
	Error          string         `yaml:"error,omitempty"`
	ExpectedMatch  *ExpectedMatch `yaml:"expected_match,omitempty"`
	AdviceScore    *AdviceScore   `yaml:"advice_score,omitempty"` // advice surface (#110)
	// FirstAttemptViolations is the validator's verdict on the first synth
	// attempt, persisted so a regression gate (#16 EG-F3) can self-diagnose
	// retry-masked regressions: a case with Attempts>1 + blocker entries
	// here names the specific cite values that triggered the retry. Empty
	// when the first attempt was clean OR no validator was wired.
	FirstAttemptViolations []FirstAttemptViolation `yaml:"first_attempt_violations,omitempty"`
}

// FirstAttemptViolation mirrors challenge.Violation but renders Severity as
// its string name in YAML (challenge.Severity is an int that would otherwise
// serialise as 0/1/2). Defined here so report.yaml / meta.yaml stay
// human-readable without depending on a stringer-aware encoder.
type FirstAttemptViolation struct {
	Severity string `yaml:"severity"`
	Code     string `yaml:"code"`
	Location string `yaml:"location"`
	Value    string `yaml:"value,omitempty"`
	Note     string `yaml:"note,omitempty"`
}

// ExpectedMatch is populated when the suite case carried an Expected block.
type ExpectedMatch struct {
	VerdictPassed     bool   `yaml:"verdict_passed"`
	BlockersPassed    bool   `yaml:"blockers_passed"`
	MustCitePassed    bool   `yaml:"must_cite_passed,omitempty"`
	MustNotCitePassed bool   `yaml:"must_not_cite_passed,omitempty"`
	Notes             string `yaml:"notes,omitempty"`
}

// AdviceScore is a retrieve --file case's outcome: per-bucket cite hits/misses
// and an overall pass (every expected cite surfaced in its bucket). olifant#110.
type AdviceScore struct {
	Avoid      BucketScore `yaml:"avoid,omitempty"`
	Prefer     BucketScore `yaml:"prefer,omitempty"`
	Convention BucketScore `yaml:"convention,omitempty"`
	Passed     bool        `yaml:"passed"`
}

// BucketScore records which expected cites surfaced in one advice bucket.
type BucketScore struct {
	Expected []string `yaml:"expected,omitempty"`
	Hit      []string `yaml:"hit,omitempty"`
	Missed   []string `yaml:"missed,omitempty"`
}

// Report is the aggregate summary written at run completion.
type Report struct {
	RunID            string       `yaml:"run_id"`
	SuiteID          string       `yaml:"suite_id"`
	StartedAt        string       `yaml:"started_at"`
	EndedAt          string       `yaml:"ended_at"`
	ElapsedMs        int64        `yaml:"elapsed_ms"`
	TotalCases       int          `yaml:"total_cases"`
	CleanCases       int          `yaml:"clean_cases"`
	TotalBlockers    int          `yaml:"total_blockers"`
	TotalWarnings    int          `yaml:"total_warnings"`
	TotalInfos       int          `yaml:"total_infos"`
	FirstTryPassRate float64      `yaml:"first_try_pass_rate"`
	GradedPassRate   *float64     `yaml:"graded_pass_rate,omitempty"` // only when Expected blocks supplied
	Cases            []CaseResult `yaml:"cases"`
}

// NewRunID constructs the lexicographic run identifier.
func NewRunID(ts time.Time, suiteID string) string {
	return ts.UTC().Format("2006-01-02T15-04-05Z") + "-" + suiteID
}
