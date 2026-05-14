// Package corpus produces the v1 corpus described in
// platform/knowledge-base/corpus/CORPUS-V1.md.
package corpus

import "time"

// Chunk is one record in an NDJSON corpus file.
type Chunk struct {
	ChunkID      string        `json:"chunk_id"`
	Source       string        `json:"source"`
	SourceSHA    string        `json:"source_sha,omitempty"`
	SourceAnchor string        `json:"source_anchor,omitempty"`
	Scope        string        `json:"scope"`
	DocType      string        `json:"doc_type"`
	ArtifactID   string        `json:"artifact_id,omitempty"`
	Title        string        `json:"title,omitempty"`
	Body         string        `json:"body"`
	Metadata     ChunkMetadata `json:"metadata"`
	EmbeddedAt   string        `json:"embedded_at,omitempty"`
}

// ChunkMetadata holds filterable fields uploaded as Chroma metadata.
type ChunkMetadata struct {
	Section       string   `json:"section,omitempty"`
	Severity      string   `json:"severity,omitempty"`
	Status        string   `json:"status,omitempty"`
	CitesInbound  []string `json:"cites_inbound,omitempty"`
	CitesOutbound []string `json:"cites_outbound,omitempty"`
	TechTags      []string `json:"tech_tags,omitempty"`
}

// Manifest captures per-source SHAs + counts so PRs show a meaningful diff.
type Manifest struct {
	BuiltAt        string           `yaml:"built_at"`
	BuilderVersion string           `yaml:"builder_version"`
	TotalChunks    int              `yaml:"total_chunks"`
	ByScope        map[string]int   `yaml:"by_scope"`
	ByDocType      map[string]int   `yaml:"by_doc_type"`
	Sources        []SourceManifest `yaml:"sources"`
}

// SourceManifest is one row per ingested source file.
type SourceManifest struct {
	Path    string `yaml:"path"`
	SHA     string `yaml:"sha"`
	Scope   string `yaml:"scope"`
	DocType string `yaml:"doc_type"`
	Chunks  int    `yaml:"chunks"`
}

// Config holds resolved paths and runtime flags.
type Config struct {
	KBRoot       string
	PlatformRoot string
	MemoryRoot   string
	OutDir       string
	Verbose      bool
}

// BuilderVersion identifies the chunking algorithm and schema version.
// Bump on any change that would produce different output for the same input.
const BuilderVersion = "olifant-corpus-v1.0.0"

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
