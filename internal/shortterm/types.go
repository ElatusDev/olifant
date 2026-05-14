// Package shortterm — Olifant's append-only event ledger. One YAML row per
// subcommand invocation. Stored under knowledge-base/short-term/turns/.
//
// Schema: knowledge-base/short-term/README.md
package shortterm

import (
	"github.com/ElatusDev/olifant/internal/challenge"
)

// TurnRecord is the on-disk shape of a single short-term memory row.
type TurnRecord struct {
	TurnID      string         `yaml:"turn_id"`
	TS          string         `yaml:"ts"`
	ParentTurn  string         `yaml:"parent_turn,omitempty"`
	Subcommand  string         `yaml:"subcommand"`
	Scope       []string       `yaml:"scope,omitempty"`
	Request     string         `yaml:"request"`

	// Exactly one of these is populated per record.
	Challenge   *ChallengeBlock   `yaml:"challenge,omitempty"`
	PromptBuild *PromptBuildBlock `yaml:"prompt_build,omitempty"`
	Validate    *ValidateBlock    `yaml:"validate,omitempty"`

	Performance PerformanceBlock `yaml:"performance"`
}

// ChallengeBlock captures what the challenge subcommand produced.
type ChallengeBlock struct {
	Verdict             string                  `yaml:"verdict"`
	Proceed             string                  `yaml:"proceed"`
	RetrievedSources    []string                `yaml:"retrieved_sources,omitempty"`
	Output              string                  `yaml:"output"`
	CiteAttempts        int                     `yaml:"cite_attempts"`
	RemainingViolations []challenge.Violation   `yaml:"remaining_violations,omitempty"`
}

// PromptBuildBlock — placeholder for the upcoming prompt-build subcommand.
type PromptBuildBlock struct {
	OutputPath     string   `yaml:"output_path,omitempty"`
	SignalsEmitted []string `yaml:"signals_emitted,omitempty"`
	PayloadBytes   int      `yaml:"payload_bytes,omitempty"`
}

// ValidateBlock — placeholder for the upcoming validate subcommand.
type ValidateBlock struct {
	ClaudeClaimCount    int      `yaml:"claude_claim_count"`
	EvidencedClaims     int      `yaml:"evidenced_claims"`
	UnmatchedClaims     []string `yaml:"unmatched_claims,omitempty"`
	StandardsSatisfied  []string `yaml:"standards_satisfied,omitempty"`
	StandardsViolated   []string `yaml:"standards_violated,omitempty"`
	DiffSHA             string   `yaml:"diff_sha,omitempty"`
	Verdict             string   `yaml:"verdict"`
}

// PerformanceBlock — common timing/metrics across all subcommands.
type PerformanceBlock struct {
	ElapsedMs    int64   `yaml:"elapsed_ms"`
	EmbedMs      int64   `yaml:"embed_ms,omitempty"`
	RetrieveMs   int64   `yaml:"retrieve_ms,omitempty"`
	SynthMs      int64   `yaml:"synth_ms,omitempty"`
	EvalTokens   int     `yaml:"eval_tokens,omitempty"`
	TokensPerSec float64 `yaml:"tokens_per_sec,omitempty"`
}
