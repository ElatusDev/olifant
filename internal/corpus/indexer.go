package corpus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// embedderMaxChars caps an input passed to the embedder. nomic-embed-text via
// Ollama rejects inputs that exceed its context (despite truncate: true).
// Empirically the effective cap is ~5000 chars; 4000 is defensive.
const embedderMaxChars = 4000

// capInput truncates s to maxChars at a UTF-8 boundary.
func capInput(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	// trim back to a safe rune boundary
	end := maxChars
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[:end]
}

// IndexConfig drives the corpus→ChromaDB upload pipeline.
type IndexConfig struct {
	CorpusDir   string // <kb-root>/corpus/v1
	OllamaURL   string
	ChromaURL   string
	Embedder    string // model name, e.g., nomic-embed-text
	Tenant      string
	Database    string
	BatchSize   int  // chunks per embed/upsert call (defaults to 32)
	Verbose     bool
	DryRun      bool // skip embedding + upserting; only walk + report
	OnlyScopes  []string
}

// IndexStats summarizes one run.
type IndexStats struct {
	ScopesProcessed int
	ChunksRead      int
	ChunksUpserted  int
	BatchesSent     int
	Elapsed         time.Duration
	PerScope        map[string]int
}

// Index reads each <scope>.ndjson, embeds each chunk via Ollama, and upserts
// into a per-scope ChromaDB collection named "corpus_<scope>".
//
// The function is idempotent — Chroma's upsert overwrites on existing IDs.
func Index(ctx context.Context, cfg IndexConfig) (IndexStats, error) {
	stats := IndexStats{PerScope: map[string]int{}}
	start := time.Now()

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

	// Smoke-test both endpoints up front — fail fast.
	if _, err := oc.Version(ctx); err != nil {
		return stats, fmt.Errorf("ollama unreachable: %w", err)
	}
	if _, err := cc.Heartbeat(ctx); err != nil {
		return stats, fmt.Errorf("chroma unreachable: %w", err)
	}
	if err := cc.EnsureTenant(ctx); err != nil {
		return stats, fmt.Errorf("chroma EnsureTenant: %w", err)
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		return stats, fmt.Errorf("chroma EnsureDatabase: %w", err)
	}

	scopes, err := discoverScopes(cfg.CorpusDir, cfg.OnlyScopes)
	if err != nil {
		return stats, err
	}

	for _, scope := range scopes {
		path := filepath.Join(cfg.CorpusDir, scope+".ndjson")
		chunks, err := readChunks(path)
		if err != nil {
			return stats, fmt.Errorf("read %s: %w", path, err)
		}
		if len(chunks) == 0 {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  %-20s skipped (0 chunks)\n", scope)
			}
			continue
		}

		collName := "corpus_" + strings.ReplaceAll(scope, "-", "_")
		coll, err := cc.EnsureCollection(ctx, collName, map[string]interface{}{
			"hnsw:space":   "cosine",
			"olifant_scope": scope,
			"created_at":   time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return stats, fmt.Errorf("EnsureCollection %s: %w", collName, err)
		}

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  %-20s %d chunks → collection %s (id=%s)\n",
				scope, len(chunks), collName, coll.ID)
		}

		if cfg.DryRun {
			stats.ChunksRead += len(chunks)
			stats.PerScope[scope] = len(chunks)
			continue
		}

		upserted, batches, err := indexScope(ctx, oc, cc, coll.ID, cfg.Embedder, chunks, cfg.BatchSize, cfg.Verbose)
		if err != nil {
			return stats, fmt.Errorf("index %s: %w", scope, err)
		}
		stats.ScopesProcessed++
		stats.ChunksRead += len(chunks)
		stats.ChunksUpserted += upserted
		stats.BatchesSent += batches
		stats.PerScope[scope] = upserted
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

// indexScope batches one scope's chunks through embed → upsert.
func indexScope(
	ctx context.Context, oc *ollama.Client, cc *chroma.Client,
	collectionID, embedder string, chunks []Chunk, batchSize int, verbose bool,
) (upserted, batches int, err error) {
	for start := 0; start < len(chunks); start += batchSize {
		end := start + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]

		// nomic-embed-text's effective input limit in Ollama is ~2048 tokens.
		// Cap each body at 6000 chars (≈ 1500 tokens conservatively) so the
		// embedder can't reject a batch on a single oversize chunk. We only
		// truncate the embedding INPUT — the stored document is the full body.
		inputs := make([]string, len(batch))
		for i, c := range batch {
			inputs[i] = capInput(c.Body, embedderMaxChars)
		}

		emb, err := oc.Embed(ctx, embedder, inputs)
		if err != nil {
			// One bad chunk shouldn't kill the whole batch. Fall back to
			// per-chunk embedding and skip any that the model rejects.
			fmt.Fprintf(os.Stderr, "    warn: batch %d..%d failed (%v); retrying per-chunk\n", start, end, err)
			emb = make([][]float32, len(batch))
			skipped := 0
			for i, in := range inputs {
				single, ierr := oc.Embed(ctx, embedder, []string{in})
				if ierr != nil || len(single) != 1 {
					fmt.Fprintf(os.Stderr, "    skip chunk %s (%d chars): %v\n",
						batch[i].ChunkID[:12], len(in), ierr)
					emb[i] = nil
					skipped++
					continue
				}
				emb[i] = single[0]
			}
			if skipped == len(batch) {
				continue
			}
		}

		// Filter out chunks whose embed was skipped.
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
		upserted += len(batch)
		if verbose && (batches%10 == 0 || end == len(chunks)) {
			fmt.Fprintf(os.Stderr, "    progress: %d/%d (batch %d)\n", upserted, len(chunks), batches)
		}
	}
	return upserted, batches, nil
}

// chunkMetadataForChroma flattens our nested chunk struct into Chroma-compatible
// metadata (scalars only). Lists become comma-joined strings; empty values are
// omitted.
func chunkMetadataForChroma(c Chunk) map[string]interface{} {
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
	if c.ArtifactID != "" {
		m["artifact_id"] = c.ArtifactID
	}
	if c.Title != "" {
		m["title"] = c.Title
	}
	if c.Metadata.Section != "" {
		m["section"] = c.Metadata.Section
	}
	if c.Metadata.Severity != "" {
		m["severity"] = c.Metadata.Severity
	}
	if c.Metadata.Status != "" {
		m["status"] = c.Metadata.Status
	}
	if len(c.Metadata.CitesOutbound) > 0 {
		m["cites_outbound"] = strings.Join(c.Metadata.CitesOutbound, ",")
	}
	if len(c.Metadata.CitesInbound) > 0 {
		m["cites_inbound"] = strings.Join(c.Metadata.CitesInbound, ",")
	}
	if len(c.Metadata.TechTags) > 0 {
		m["tech_tags"] = strings.Join(c.Metadata.TechTags, ",")
	}
	return m
}

// readChunks parses a scope NDJSON file into Chunk values.
func readChunks(path string) ([]Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var out []Chunk
	for scan.Scan() {
		var c Chunk
		if err := json.Unmarshal(scan.Bytes(), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, scan.Err()
}

// discoverScopes lists scopes either from the corpus dir contents or the
// `only` filter when supplied.
func discoverScopes(corpusDir string, only []string) ([]string, error) {
	if len(only) > 0 {
		return only, nil
	}
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, fmt.Errorf("read corpus dir %s: %w", corpusDir, err)
	}
	var scopes []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		scopes = append(scopes, strings.TrimSuffix(name, ".ndjson"))
	}
	return scopes, nil
}
