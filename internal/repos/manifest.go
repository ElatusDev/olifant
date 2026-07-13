// Repo-family manifest (olifant#82, D-FF2/D-FF3): the code_* collections get
// the same landed-manifest + diff discipline the corpus family got in D228.
// The manifest reuses corpus.Manifest verbatim — one on-disk shape, one diff
// engine (corpus.DiffManifests) — with source identity `<repo>/<relPath>`
// (repo-qualified, matching chunk `source` metadata, so DeleteWhere is safe
// in shared-scope collections like code_mobile's two repos).
package repos

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ElatusDev/olifant/internal/corpus"
)

// SyncBuilderVersion marks the repo chunking algorithm. Bump on ANY change
// to Walk/Chunk semantics (window size, overlap, breadcrumb, skip rules) —
// a source diff cannot express a chunking change (the D228 D-CS3 rule).
const SyncBuilderVersion = "olifant-repo-v1.0.0"

// ManifestPath is the committed location of the repo-family manifest,
// beside the corpus manifest so the nightly auto-PR lands both and the
// eval-gate fingerprint reaches it with one FileSHA256 (GD-1b).
func ManifestPath(kbRoot string) string {
	return filepath.Join(kbRoot, "corpus", "v1", "repo-manifest.yaml")
}

// CollectChunks walks + chunks every configured repo WITHOUT touching the
// network — the shared front half of Ingest, Sync, and Status.
func CollectChunks(cfg IngestConfig) (map[string][]corpus.Chunk, IngestStats, error) {
	stats := IngestStats{
		PerRepo:  map[string]int{},
		PerScope: map[string]int{},
	}
	scoped := map[string][]corpus.Chunk{}

	for _, rs := range cfg.Repos {
		if _, err := os.Stat(rs.Path); err != nil {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  skip %s: %v\n", rs.Name, err)
			}
			continue
		}
		files, err := Walk(rs.Path, rs.Name)
		if err != nil {
			return nil, stats, err
		}
		stats.FilesRead += len(files)

		var repoChunks []corpus.Chunk
		for _, f := range files {
			cs := Chunk(f, rs.Scope)
			if len(cs) == 0 {
				stats.FilesSkipped++
				continue
			}
			repoChunks = append(repoChunks, cs...)
		}
		stats.ChunksProduced += len(repoChunks)
		stats.PerRepo[rs.Name] = len(repoChunks)
		stats.PerScope[rs.Scope] += len(repoChunks)
		scoped[rs.Scope] = append(scoped[rs.Scope], repoChunks...)

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  %-22s files=%-5d chunks=%-6d scope=%s\n",
				rs.Name, len(files), len(repoChunks), rs.Scope)
		}
	}

	// Deterministic order per scope (sorted by source, then anchor).
	for scope := range scoped {
		cs := scoped[scope]
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].Source != cs[j].Source {
				return cs[i].Source < cs[j].Source
			}
			return cs[i].SourceAnchor < cs[j].SourceAnchor
		})
	}
	stats.ReposProcessed = len(cfg.Repos)
	return scoped, stats, nil
}

// BuildManifest derives the repo-family manifest from collected chunks.
// Deterministic on unchanged input except BuiltAt, which DiffManifests
// ignores by design.
func BuildManifest(scoped map[string][]corpus.Chunk) corpus.Manifest {
	perSource := map[string]*corpus.SourceManifest{}
	var order []string

	total := 0
	byScope := map[string]int{}
	byDocType := map[string]int{}
	for scope, chunks := range scoped {
		byScope[scope] += len(chunks)
		total += len(chunks)
		for i := range chunks {
			c := &chunks[i]
			byDocType[c.DocType]++
			sm, ok := perSource[c.Source]
			if !ok {
				sm = &corpus.SourceManifest{
					Path:    c.Source,
					SHA:     c.SourceSHA,
					Scope:   c.Scope,
					DocType: c.DocType,
				}
				perSource[c.Source] = sm
				order = append(order, c.Source)
			}
			sm.Chunks++
		}
	}
	sort.Strings(order)

	m := corpus.Manifest{
		BuiltAt:        time.Now().UTC().Format(time.RFC3339),
		BuilderVersion: SyncBuilderVersion,
		TotalChunks:    total,
		ByScope:        byScope,
		ByDocType:      byDocType,
	}
	for _, p := range order {
		m.Sources = append(m.Sources, *perSource[p])
	}
	return m
}
