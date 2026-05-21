package corpus

import "time"

// Symbol is one extracted identifier from a source file. Tags hold the
// multi-axis classification per the v2 curriculum workflow (D-CC1).
//
// Tag values are `any` so an axis can be single-valued (string) or
// multi-valued ([]string for AxisConcern). YAML marshaling preserves
// both shapes via gopkg.in/yaml.v3.
//
// Distinct from corpus.Chunk (defined in types.go) which is the
// pre-existing v1 ChromaDB corpus row.
type Symbol struct {
	ID     string         `yaml:"id"`     // stable hash of (source, line, text)
	Text   string         `yaml:"text"`   // the identifier as it appears
	Source string         `yaml:"source"` // file path relative to repo root
	Line   int            `yaml:"line"`
	Tags   map[string]any `yaml:"tags"`   // axis name → value
}

// Sentence is one extracted clause from a Markdown/doc file. Same tag
// shape as Symbol but uses sentence-only axes (AxisSyntactic,
// AxisSemanticRole, AxisModality, AxisSubjectRef).
type Sentence struct {
	ID     string         `yaml:"id"`
	Text   string         `yaml:"text"`
	Source string         `yaml:"source"`
	Line   int            `yaml:"line"`
	Tags   map[string]any `yaml:"tags"`
}

// ScanConfig drives `olifant corpus scan` for one repo + module/feature
// slice. Per-extractor implementations dispatch by repo/language.
type ScanConfig struct {
	Repo       string // logical repo name (core-api, akademia-plus-web, ...)
	RepoRoot   string // absolute path to repo root
	Module     string // module/feature name (optional for repos w/o module structure)
	SourceRoot string // absolute path inside repo to start the walk
	OutPath    string // YAML output path
	DryRun     bool
	Verbose    bool
}

// ScanStats summarises one scan run for the manifest + CLI output.
type ScanStats struct {
	FilesScanned   int            `yaml:"files_scanned"`
	SymbolsEmitted int            `yaml:"symbols_emitted"`
	ByKind         map[string]int `yaml:"by_kind"`
	ByConcern      map[string]int `yaml:"by_concern"`
	Elapsed        time.Duration  `yaml:"elapsed_ms"`
}

// ScanManifestEntry is one row in the per-repo or per-run manifest,
// telling callers "this YAML came from this run + these inputs".
// Distinct from corpus.Manifest (v1) and corpus.SourceManifest (v1).
type ScanManifestEntry struct {
	RunID       string    `yaml:"run_id"`
	GeneratedAt string    `yaml:"generated_at"`
	Repo        string    `yaml:"repo"`
	Module      string    `yaml:"module,omitempty"`
	SourceRoot  string    `yaml:"source_root"`
	SourceSHA   string    `yaml:"source_sha,omitempty"`
	Stats       ScanStats `yaml:"stats"`
}
