// Package dataset extracts Tier 1 (curated reasoning prose) and Tier 2
// (lifecycle triples) training examples from the knowledge-base.
//
// Spec: knowledge-base/architecture/olifant-training-plan.md §2-§4.
//
// Each extractor reads one source family (retros, decisions,
// anti-patterns, patterns, lifecycle triples) and emits ShareGPT-style
// JSONL rows. The package is independent of `history` (Tier 3) — they
// share the JSONL row shape via duplicated structs by design, same
// pattern as history mirroring internal/repos types.
package dataset

import "time"

// olifantSystemPrompt mirrors history.olifantSystemPrompt so all
// training data carries one consistent system message.
const olifantSystemPrompt = "You are Olifant — domain expert for ElatusDev/AkademiaPlus."

// BuilderVersion identifies the extractor algorithm. Bump on any
// change that would alter output for the same KB input.
const BuilderVersion = "olifant-dataset-v1.0.0-dev"

// Example is the on-disk training-data row, ShareGPT-style per
// olifant-training-plan.md §3.
type Example struct {
	System   string            `json:"system"`
	Messages []ChatMessage     `json:"messages"`
	Tier     int               `json:"tier"`
	Scope    string            `json:"scope"`
	Source   string            `json:"source"` // path or stable ID relative to kb-root
	Role     string            `json:"role"`   // "domain" | "challenge" | "prompt_build" | "validate"
	Family   string            `json:"family"` // "retro-section" | "decision-qa" | "antipattern-challenge" | "pattern-section" | "lifecycle-triple"
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ChatMessage is one turn in a ShareGPT example.
type ChatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// SourceKind enumerates extractor source families.
type SourceKind string

const (
	SourceRetros        SourceKind = "retros"
	SourceDecisions     SourceKind = "decisions"
	SourceAntipatterns  SourceKind = "antipatterns"
	SourcePatterns      SourceKind = "patterns"
	SourceTriples       SourceKind = "triples"
)

// AllSources is the canonical ordering.
var AllSources = []SourceKind{
	SourceRetros,
	SourceDecisions,
	SourceAntipatterns,
	SourcePatterns,
	SourceTriples,
}

// Tier maps a source to its training-plan tier.
func (s SourceKind) Tier() int {
	switch s {
	case SourceRetros, SourceDecisions, SourceAntipatterns, SourcePatterns:
		return 1
	case SourceTriples:
		return 2
	default:
		return 0
	}
}

// SubDir returns the per-source output subdirectory under <outDir>.
func (s SourceKind) SubDir() string {
	return "tier" + itoa(s.Tier()) + "-" + string(s)
}

// BuildConfig drives `olifant dataset build`.
type BuildConfig struct {
	KBRoot   string       // knowledge-base root (required)
	OutDir   string       // <kb-root>/training/<YYYY-MM-DD>/  — per-source subdirs created beneath
	Sources  []SourceKind // sources to extract; empty = AllSources
	WriteJSONL bool       // emit JSONL files (default true)
	Verbose  bool
}

// BuildStats summarizes one run. PerSource keys are SourceKind.String().
type BuildStats struct {
	SourcesProcessed int
	ExamplesEmitted  int
	PerSource        map[string]SourceStats
	Elapsed          time.Duration
}

// SourceStats are the per-source counters surfaced in manifest.yaml
// and the CLI summary.
type SourceStats struct {
	FilesScanned    int            `yaml:"files_scanned"`
	EntriesParsed   int            `yaml:"entries_parsed"`
	ExamplesEmitted int            `yaml:"examples_emitted"`
	PerScope        map[string]int `yaml:"per_scope"` // scope → emitted count
}

// Manifest is the on-disk record of one build run.
type Manifest struct {
	RunID          string                 `yaml:"run_id"`
	BuilderVersion string                 `yaml:"builder_version"`
	GeneratedAt    string                 `yaml:"generated_at"`
	KBRoot         string                 `yaml:"kb_root"`
	OutDir         string                 `yaml:"out_dir"`
	Sources        []string               `yaml:"sources"`
	Totals         ManifestTotals         `yaml:"totals"`
	PerSource      map[string]SourceStats `yaml:"per_source"`
}

// ManifestTotals roll up totals across all sources.
type ManifestTotals struct {
	SourcesProcessed int `yaml:"sources_processed"`
	ExamplesEmitted  int `yaml:"examples_emitted"`
	ElapsedMs        int `yaml:"elapsed_ms"`
}

// itoa is a tiny helper to avoid pulling strconv into the SubDir hot
// path. Used only for single-digit tier numbers.
func itoa(n int) string {
	if n < 0 || n > 9 {
		return ""
	}
	return string(rune('0' + n))
}
