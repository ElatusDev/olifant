// Package history walks repo commit history and emits two JSONL
// families per repo:
//
//	commit-summaries — one row per commit: subject + body + all files
//	                   touched (capped at 10 with overflow marker) +
//	                   cite IDs.
//	file-snapshots   — one row per (commit × file-touched): full file
//	                   content at the post-commit tree (32KB cap with
//	                   truncation marker) + full unified diff for that
//	                   file + commit context.
//
// Together these give RAG + future fine-tune both pattern signal
// (what the code looked like at a point in time) and evolution
// signal (how it changed). Spec: knowledge-base/architecture/
// olifant-training-plan.md §2 Tier 3.
//
// ChromaDB integration arrives in Phase 3 (history_<scope> +
// code_history_<scope> collections).
package history

import "time"

// CommitRecord is the in-memory representation of one git commit
// after walking + parsing. Drives both JSONL families and (Phase 3)
// ChromaDB chunk synthesis.
type CommitRecord struct {
	Repo        string         // e.g., "core-api"
	Scope       string         // backend | webapp | mobile | e2e | infra
	SHA         string         // full 40-char hex
	ShortSHA    string         // first 7 chars
	ParentSHA   string         // first-parent SHA used for diff
	Author      string         // "Name <email>"
	CommittedAt time.Time      // commit timestamp (UTC)
	Subject     string         // first line of commit message
	Body        string         // remaining message body (trimmed, attribution-stripped)
	Files       []FileTouch    // every file touched in this commit
	Snapshots   []FileSnapshot // per-file full content + diff
	CiteIDs     []string       // artifact IDs extracted from message (D17, AP14, SB-04, …)
}

// FileTouch summarizes one file's change in a commit.
type FileTouch struct {
	Path      string
	Status    string // "added" | "modified" | "deleted" | "renamed"
	OldPath   string // pre-commit path if renamed, else ""
	Additions int
	Deletions int
}

// FileSnapshot is the full pattern+evolution record for one file at
// one commit: its content as of the commit tree + the unified diff
// that produced it. For deleted files, Content holds the
// pre-deletion content from the parent tree.
type FileSnapshot struct {
	Path             string // post-commit path (or pre-deletion path if deleted)
	OldPath          string // pre-commit path if renamed, else ""
	Status           string // "added" | "modified" | "deleted" | "renamed"
	Content          string // file content at the commit tree, post-cap
	ContentSize      int    // original byte count pre-cap
	ContentTruncated bool   // true if Content was truncated
	DiffUnified      string // full per-file unified diff including @@ headers + context lines
	DiffSize         int    // bytes of DiffUnified pre-cap
	DiffTruncated    bool   // true if DiffUnified was truncated
}

// JSONLExample is the on-disk training-data row. ShareGPT-style chat
// messages per olifant-training-plan.md §3.
type JSONLExample struct {
	System   string            `json:"system"`
	Messages []ChatMessage     `json:"messages"`
	Tier     int               `json:"tier"`
	Scope    string            `json:"scope"`
	Source   string            `json:"source"` // "<repo>@<short-sha>" or "<repo>@<short-sha>:<path>"
	Role     string            `json:"role"`   // "validate" (commit) | "domain" (file-snapshot)
	Family   string            `json:"family"` // "commit-summary" | "file-snapshot"
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ChatMessage is one turn in a ShareGPT example.
type ChatMessage struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"`
}

// ScanConfig drives `olifant history scan`.
type ScanConfig struct {
	Repos           []RepoSpec
	Since           time.Time // hard floor on commit date
	OutDir          string    // <kb-root>/training/<YYYY-MM-DD>/tier3-history/
	WriteJSONL      bool      // emit JSONL files (default true)
	ContentCapBytes int       // bytes per file snapshot content cap (default 32_768)
	DiffCapBytes    int       // bytes per file unified diff cap (default 16_384)
	FilesListCap    int       // max files in commit-summary list (default 10)
	ManifestPath    string    // where to read/write the incremental-scan manifest
	FullScan        bool      // ignore manifest last_sha; rescan from since-floor
	WriteManifest   bool      // update manifest after successful scan (default true)
	Verbose         bool
	DryRun          bool // walk + parse only; no JSONL write
}

// ScanStats summarizes one run.
type ScanStats struct {
	ReposProcessed     int
	CommitsWalked      int
	CommitsEmitted     int
	CommitsSkipped     int
	SnapshotsEmitted   int
	SnapshotsTruncated int // count of file snapshots whose content was truncated
	PerRepo            map[string]int // repo → emitted commit count
	PerScope           map[string]int // scope → emitted commit count
	Elapsed            time.Duration
}

// RepoSpec couples a repo directory with its scope. Mirrors the
// internal/repos.RepoSpec shape so the two packages stay aligned but
// independent — history is per-commit, repos is per-file.
type RepoSpec struct {
	Name  string
	Path  string
	Scope string
}

// BuilderVersion identifies the walker+parser+emitter algorithm. Bump
// on any change that would alter output for the same commit input.
const BuilderVersion = "olifant-history-v1.0.0-dev"

// DefaultSince is the project-agreed baseline: only commits on or
// after 2026-01-01 are considered.
var DefaultSince = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Default caps.
const (
	DefaultContentCapBytes = 32 * 1024
	DefaultDiffCapBytes    = 16 * 1024
	DefaultFilesListCap    = 10
)
