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
	TurnID     string   `yaml:"turn_id"`
	TS         string   `yaml:"ts"`
	ParentTurn string   `yaml:"parent_turn,omitempty"`
	Subcommand string   `yaml:"subcommand"`
	Scope      []string `yaml:"scope,omitempty"`
	Request    string   `yaml:"request"`

	// Exactly one of these is populated per record.
	Challenge     *ChallengeBlock     `yaml:"challenge,omitempty"`
	PromptBuild   *PromptBuildBlock   `yaml:"prompt_build,omitempty"`
	PromptContext *PromptContextBlock `yaml:"prompt_context,omitempty"`
	PromptCheck   *PromptCheckBlock   `yaml:"prompt_check,omitempty"`
	Retrieve      *RetrieveBlock      `yaml:"retrieve,omitempty"`
	Validate      *ValidateBlock      `yaml:"validate,omitempty"`
	Digest        *DigestBlock        `yaml:"digest,omitempty"`

	Performance PerformanceBlock `yaml:"performance"`
}

// ChallengeBlock captures what the challenge subcommand produced.
type ChallengeBlock struct {
	Verdict             string                `yaml:"verdict"`
	Proceed             string                `yaml:"proceed"`
	RetrievedSources    []string              `yaml:"retrieved_sources,omitempty"`
	Output              string                `yaml:"output"`
	CiteAttempts        int                   `yaml:"cite_attempts"`
	RemainingViolations []challenge.Violation `yaml:"remaining_violations,omitempty"`
}

// PromptBuildBlock — placeholder for the upcoming prompt-build subcommand.
type PromptBuildBlock struct {
	OutputPath     string   `yaml:"output_path,omitempty"`
	SignalsEmitted []string `yaml:"signals_emitted,omitempty"`
	PayloadBytes   int      `yaml:"payload_bytes,omitempty"`
}

// PromptContextBlock captures what `prompt context` retrieved (no synthesis).
type PromptContextBlock struct {
	RetrievedCount int      `yaml:"retrieved_count"`
	Sources        []string `yaml:"sources,omitempty"`
	PayloadBytes   int      `yaml:"payload_bytes,omitempty"`
}

// PromptCheckBlock captures a `prompt check` cite-gate verdict on a document.
type PromptCheckBlock struct {
	DocPath         string   `yaml:"doc_path"`
	Verdict         string   `yaml:"verdict"`
	Resolved        int      `yaml:"resolved"`
	Stale           int      `yaml:"stale"`
	Unresolved      int      `yaml:"unresolved"`
	UnresolvedCites []string `yaml:"unresolved_cites,omitempty"`
}

// RetrieveBlock captures what `retrieve` returned plus the token-economy
// measurement (payload vs the cited source docs' total bytes — charter R5).
type RetrieveBlock struct {
	Inferred       bool     `yaml:"inferred,omitempty"`
	RetrievedCount int      `yaml:"retrieved_count"`
	Sources        []string `yaml:"sources,omitempty"`
	PayloadBytes   int      `yaml:"payload_bytes,omitempty"`
	SourceBytes    int64    `yaml:"source_bytes,omitempty"`
}

// DigestBlock captures one `digest` run (charter R6): the compaction
// measurement plus the validation verdict; cache hits are recorded too so
// digest usage is fully labelable (D-DG4).
type DigestBlock struct {
	Source    string  `yaml:"source"`
	SourceSHA string  `yaml:"source_sha"`
	BytesIn   int     `yaml:"bytes_in"`
	BytesOut  int     `yaml:"bytes_out"`
	Ratio     float64 `yaml:"ratio"`
	CacheHit  bool    `yaml:"cache_hit,omitempty"`
	Attempts  int     `yaml:"attempts,omitempty"`
	Model     string  `yaml:"model,omitempty"`
}

// ValidateBlock captures a `validate` run richly enough to reconstruct a
// runnable eval case (olifant#86, HV-F2) — mirroring ChallengeBlock's
// reconstructable detail. Pre-#86 records carry only Verdict; the added
// fields read as zero on old rows (additive, back-compat). Lives under
// short-term/ (firewalled from the corpus, D-BK9) so storing the model's
// assessment here never leaks into retrieval.
type ValidateBlock struct {
	Claim                  string                `yaml:"claim,omitempty"`    // full claim text — the runnable-case seed (Request stays display-truncated)
	Diff                   string                `yaml:"diff,omitempty"`     // full diff text — the frozen snapshot a harvested case reproduces from (D-VC3)
	ClaudeClaimCount       int                   `yaml:"claude_claim_count"` // atomic claims parsed
	EvidencedClaims        int                   `yaml:"evidenced_claims"`
	PartialClaims          int                   `yaml:"partial_claims,omitempty"`
	UnmatchedClaims        []string              `yaml:"unmatched_claims,omitempty"`
	StandardsSatisfied     []string              `yaml:"standards_satisfied,omitempty"`
	StandardsViolated      []string              `yaml:"standards_violated,omitempty"`
	Cites                  []string              `yaml:"cites,omitempty"` // union of assessment cites — seeds the expected `must_cite_any_of` skeleton
	RetrievedSources       []string              `yaml:"retrieved_sources,omitempty"`
	Proceed                string                `yaml:"proceed,omitempty"`
	ValidateAttempts       int                   `yaml:"validate_attempts,omitempty"`        // 1 = clean first try; 2+ = retried
	FirstAttemptViolations []challenge.Violation `yaml:"first_attempt_violations,omitempty"` // EG-F3: retry-masked regressions
	DiffSHA                string                `yaml:"diff_sha,omitempty"`
	Verdict                string                `yaml:"verdict"`
	Output                 string                `yaml:"output,omitempty"` // full assessment RawJSON (forensics; challenge parity)
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
