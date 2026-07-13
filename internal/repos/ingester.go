package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// RepoSpec couples a platform repo directory with its scope.
type RepoSpec struct {
	Name  string // e.g., "core-api"
	Path  string // absolute path
	Scope string // "backend" | "webapp" | "mobile" | "e2e" | "infra"
}

// DefaultRepos returns the platform's seven repos rooted under platformRoot.
// Order is deterministic — smallest-first so failures surface quickly.
func DefaultRepos(platformRoot string) []RepoSpec {
	return []RepoSpec{
		{Name: "infra", Path: filepath.Join(platformRoot, "infra"), Scope: "infra"},
		{Name: "core-api-e2e", Path: filepath.Join(platformRoot, "core-api-e2e"), Scope: "e2e"},
		{Name: "akademia-plus-go", Path: filepath.Join(platformRoot, "akademia-plus-go"), Scope: "mobile"},
		{Name: "akademia-plus-central", Path: filepath.Join(platformRoot, "akademia-plus-central"), Scope: "mobile"},
		{Name: "elatusdev-web", Path: filepath.Join(platformRoot, "elatusdev-web"), Scope: "webapp"},
		{Name: "akademia-plus-web", Path: filepath.Join(platformRoot, "akademia-plus-web"), Scope: "webapp"},
		{Name: "core-api", Path: filepath.Join(platformRoot, "core-api"), Scope: "backend"},
	}
}

// IngestConfig drives `olifant repo ingest`.
type IngestConfig struct {
	Repos        []RepoSpec
	OutDir       string // <kb-root>/corpus/v1/code (NDJSON output)
	WriteNDJSON  bool   // write per-scope NDJSON in addition to ChromaDB upsert
	ManifestPath string // repo-family manifest, written LAST (olifant#82); "" = skip
	OllamaURL    string
	ChromaURL    string
	Embedder     string
	Tenant       string
	Database     string
	BatchSize    int
	Verbose      bool
	DryRun       bool // walk + chunk; no embed, no upsert, no write
}

// IngestStats summarizes one run.
type IngestStats struct {
	ReposProcessed int
	FilesRead      int
	FilesSkipped   int
	ChunksProduced int
	ChunksUpserted int
	BatchesSent    int
	PerRepo        map[string]int
	PerScope       map[string]int
	Elapsed        time.Duration
}

// Ingest is the entry point. For each repo: walk → chunk → embed → upsert.
// Writes optional NDJSON per scope so the corpus is rebuildable without
// re-walking sources.
func Ingest(ctx context.Context, cfg IngestConfig) (IngestStats, error) {
	stats := IngestStats{
		PerRepo:  map[string]int{},
		PerScope: map[string]int{},
	}
	start := time.Now()

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.WriteNDJSON && cfg.OutDir != "" {
		if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
			return stats, fmt.Errorf("mkdir %s: %w", cfg.OutDir, err)
		}
	}

	// A manifest minted over a partially-present repo set would record a
	// partial source universe; the next sync would delete the rest.
	if cfg.ManifestPath != "" && !cfg.DryRun {
		if missing := missingRepoDirs(cfg.Repos); len(missing) > 0 {
			return stats, fmt.Errorf("ingest: repo dir(s) missing: %s — refusing to mint a partial manifest (restore the checkout, or filter with -repo to skip minting)", strings.Join(missing, ", "))
		}
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

	if !cfg.DryRun {
		if _, err := oc.Version(ctx); err != nil {
			return stats, fmt.Errorf("ollama unreachable: %w", err)
		}
		if _, err := cc.Heartbeat(ctx); err != nil {
			return stats, fmt.Errorf("chroma unreachable: %w", err)
		}
		if err := cc.EnsureTenant(ctx); err != nil {
			return stats, err
		}
		if err := cc.EnsureDatabase(ctx); err != nil {
			return stats, err
		}
	}

	// Walk + chunk (shared with Sync/Status — olifant#82).
	scopedChunks, walkStats, err := CollectChunks(cfg)
	if err != nil {
		return stats, fmt.Errorf("walk: %w", err)
	}
	stats.FilesRead = walkStats.FilesRead
	stats.FilesSkipped = walkStats.FilesSkipped
	stats.ChunksProduced = walkStats.ChunksProduced
	stats.PerRepo = walkStats.PerRepo
	stats.PerScope = walkStats.PerScope

	if cfg.DryRun {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}

	// Per scope (already sorted by CollectChunks): write NDJSON, then
	// embed + upsert in batches.
	for scope, chunks := range scopedChunks {
		if cfg.WriteNDJSON && cfg.OutDir != "" {
			ndjsonPath := filepath.Join(cfg.OutDir, scope+".ndjson")
			if err := writeChunksNDJSON(ndjsonPath, chunks); err != nil {
				return stats, fmt.Errorf("write %s: %w", ndjsonPath, err)
			}
		}

		coll, err := cc.EnsureCollection(ctx, "code_"+strings.ReplaceAll(scope, "-", "_"),
			map[string]interface{}{
				"hnsw:space":    "cosine",
				"olifant_scope": scope,
				"olifant_kind":  "code",
				"created_at":    time.Now().UTC().Format(time.RFC3339),
			})
		if err != nil {
			return stats, fmt.Errorf("EnsureCollection code_%s: %w", scope, err)
		}

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  upserting %d chunks → code_%s (id=%s)\n",
				len(chunks), scope, coll.ID)
		}

		ups, batches, err := indexBatched(ctx, oc, cc, coll.ID, cfg.Embedder, chunks, cfg.BatchSize, cfg.Verbose)
		if err != nil {
			return stats, fmt.Errorf("index code_%s: %w", scope, err)
		}
		stats.ChunksUpserted += ups
		stats.BatchesSent += batches
	}

	// Manifest LAST (olifant#82 D-FF1/D-FF2): a full ingest doubles as the
	// manifest bootstrap; an interrupted run leaves the old manifest (or
	// none) so the next sync re-diffs and repairs.
	if cfg.ManifestPath != "" {
		if err := corpus.WriteManifest(cfg.ManifestPath, BuildManifest(scopedChunks)); err != nil {
			return stats, fmt.Errorf("write repo manifest: %w", err)
		}
	}

	stats.ReposProcessed = len(cfg.Repos)
	stats.Elapsed = time.Since(start)
	return stats, nil
}

// indexBatched batches embed → upsert with per-chunk fallback on batch failure.
// Pattern mirrors corpus/indexer.go to keep behavior consistent.
func indexBatched(
	ctx context.Context, oc *ollama.Client, cc *chroma.Client,
	collectionID, embedder string, chunks []corpus.Chunk, batchSize int, verbose bool,
) (upserted, batches int, err error) {
	// nomic-embed-text has a 2048-token default context. At ~1 char/token
	// worst-case (dense code with many short tokens), 2000 chars stays
	// under the ceiling. Mirrors history/index.go.embedderMaxChars.
	const embedderMaxChars = 2000

	for start := 0; start < len(chunks); start += batchSize {
		end := start + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]

		inputs := make([]string, len(batch))
		for i, c := range batch {
			inputs[i] = capChars(c.Body, embedderMaxChars)
		}

		emb, eerr := oc.Embed(ctx, embedder, inputs)
		if eerr != nil {
			// Drop idle keep-alives before per-chunk retry so the
			// fallback never reuses a half-dead connection (the P5
			// Tailscale read-timeout symptom from the history track).
			oc.CloseIdle()
			fmt.Fprintf(os.Stderr, "    warn: batch %d..%d failed (%v); reset conn pool, retrying per-chunk\n", start, end, eerr)
			emb = make([][]float32, len(batch))
			for i, in := range inputs {
				single, ierr := oc.Embed(ctx, embedder, []string{in})
				if ierr != nil || len(single) != 1 {
					fmt.Fprintf(os.Stderr, "    skip chunk %s (%d chars): %v\n",
						batch[i].ChunkID[:12], len(in), ierr)
					emb[i] = nil
					continue
				}
				emb[i] = single[0]
			}
		}

		ids := make([]string, 0, len(batch))
		docs := make([]string, 0, len(batch))
		metas := make([]map[string]interface{}, 0, len(batch))
		filtered := make([][]float32, 0, len(batch))
		for i, c := range batch {
			if emb[i] == nil {
				continue
			}
			ids = append(ids, c.ChunkID)
			docs = append(docs, c.Body)
			metas = append(metas, chunkMetadataForChroma(c))
			filtered = append(filtered, emb[i])
		}
		emb = filtered
		if len(ids) == 0 {
			continue
		}

		if err := cc.Upsert(ctx, collectionID, chroma.UpsertRequest{
			IDs:        ids,
			Embeddings: emb,
			Documents:  docs,
			Metadatas:  metas,
		}); err != nil {
			return upserted, batches, fmt.Errorf("upsert batch %d..%d: %w", start, end, err)
		}
		batches++
		upserted += len(ids)
		if verbose && (batches%10 == 0 || end == len(chunks)) {
			fmt.Fprintf(os.Stderr, "    progress: %d/%d (batch %d)\n", upserted, len(chunks), batches)
		}
	}
	return upserted, batches, nil
}

// chunkMetadataForChroma — flattens a Chunk into Chroma-compatible scalar
// metadata. Mirrors the corpus indexer; replicated here to keep the package
// boundary clean.
func chunkMetadataForChroma(c corpus.Chunk) map[string]interface{} {
	m := map[string]interface{}{
		"source":   c.Source,
		"scope":    c.Scope,
		"doc_type": c.DocType,
	}
	if c.SourceSHA != "" {
		m["source_sha"] = c.SourceSHA
	}
	if c.SourceAnchor != "" {
		m["source_anchor"] = c.SourceAnchor
	}
	if c.Title != "" {
		m["title"] = c.Title
	}
	if c.Metadata.Section != "" {
		m["section"] = c.Metadata.Section
	}
	if len(c.Metadata.CitesOutbound) > 0 {
		m["cites_outbound"] = strings.Join(c.Metadata.CitesOutbound, ",")
	}
	if len(c.Metadata.TechTags) > 0 {
		m["tech_tags"] = strings.Join(c.Metadata.TechTags, ",")
	}
	return m
}

func writeChunksNDJSON(path string, chunks []corpus.Chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for i := range chunks {
		if err := enc.Encode(&chunks[i]); err != nil {
			return err
		}
	}
	return nil
}

// capChars trims s to maxChars at a UTF-8 boundary.
func capChars(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	end := maxChars
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[:end]
}
