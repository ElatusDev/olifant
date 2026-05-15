package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// IndexConfig drives Index. Ollama and Chroma endpoints must be
// reachable from the host running the index step.
type IndexConfig struct {
	OllamaURL    string // e.g., "http://olifant:11434"
	ChromaURL    string // e.g., "http://localhost:8000" (typically a port-forward)
	ChromaTenant string // e.g., "default_tenant"
	ChromaDB     string // e.g., "default_database"
	Embedder     string // e.g., "nomic-embed-text"
	BatchSize    int    // chunks per embed call; 0 → 32
	Verbose      bool
	DryRun       bool // build chunks only; no embed, no upsert
}

// IndexStats summarizes one Index call.
type IndexStats struct {
	CommitChunks     int            // history_<scope> chunks produced
	SnapshotChunks   int            // code_history_<scope> chunks produced
	CommitUpserted   int            // chunks successfully upserted into history_<scope>
	SnapshotUpserted int            // chunks successfully upserted into code_history_<scope>
	BatchesSent      int            // total embed/upsert batches across all collections
	PerCollection    map[string]int // collection name → upserted chunks
	Elapsed          time.Duration
}

// Two collection families this package writes to. Names are
// constructed as "<prefix>_<scope>" with scope sanitized for the
// ChromaDB collection-name grammar (no hyphens).
const (
	commitCollectionPrefix   = "history"
	snapshotCollectionPrefix = "code_history"
)

// Index embeds the records' commit summaries and per-file snapshots
// and upserts them into two ChromaDB collection families:
//
//	history_<scope>       — one chunk per commit (summary text)
//	code_history_<scope>  — one chunk per (commit, file) (snapshot content)
//
// Together these give the RAG retriever both pattern signal (what
// the code looked like at a specific commit) and evolution signal
// (what changed in that commit and why).
//
// Idempotent: chunk_ids are deterministic hashes of (repo, sha, path).
// Re-running Index on the same records is a no-op upsert in
// ChromaDB.
func Index(ctx context.Context, records []*CommitRecord, cfg IndexConfig) (*IndexStats, error) {
	stats := &IndexStats{PerCollection: map[string]int{}}
	start := time.Now()

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Embedder == "" {
		cfg.Embedder = "nomic-embed-text"
	}

	commitsByScope, snapshotsByScope := groupChunksByScope(records)
	for _, cs := range commitsByScope {
		stats.CommitChunks += len(cs)
	}
	for _, ss := range snapshotsByScope {
		stats.SnapshotChunks += len(ss)
	}

	if cfg.DryRun {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.ChromaTenant, cfg.ChromaDB)

	if _, err := oc.Version(ctx); err != nil {
		return stats, fmt.Errorf("history.Index: ollama unreachable: %w", err)
	}
	if _, err := cc.Heartbeat(ctx); err != nil {
		return stats, fmt.Errorf("history.Index: chroma unreachable: %w", err)
	}
	if err := cc.EnsureTenant(ctx); err != nil {
		return stats, err
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		return stats, err
	}

	// history_<scope> — commit summaries
	if err := indexFamily(ctx, oc, cc, cfg, commitsByScope, commitCollectionPrefix, "commit-summary", stats, true); err != nil {
		return stats, err
	}
	// code_history_<scope> — file snapshots
	if err := indexFamily(ctx, oc, cc, cfg, snapshotsByScope, snapshotCollectionPrefix, "file-snapshot", stats, false); err != nil {
		return stats, err
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

// indexFamily ensures the per-scope collection exists then embed+upserts
// every chunk in the family. The isCommit flag steers whether to tally
// upserts into stats.CommitUpserted or stats.SnapshotUpserted.
func indexFamily(
	ctx context.Context,
	oc *ollama.Client,
	cc *chroma.Client,
	cfg IndexConfig,
	chunksByScope map[string][]corpus.Chunk,
	collectionPrefix, kind string,
	stats *IndexStats,
	isCommit bool,
) error {
	scopes := make([]string, 0, len(chunksByScope))
	for s := range chunksByScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	for _, scope := range scopes {
		chunks := chunksByScope[scope]
		if len(chunks) == 0 {
			continue
		}
		collName := collectionPrefix + "_" + strings.ReplaceAll(scope, "-", "_")
		coll, err := cc.EnsureCollection(ctx, collName, map[string]interface{}{
			"hnsw:space":    "cosine",
			"olifant_scope": scope,
			"olifant_kind":  kind,
			"created_at":    time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("EnsureCollection %s: %w", collName, err)
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  upserting %d chunks → %s (id=%s)\n", len(chunks), collName, coll.ID)
		}
		ups, batches, err := embedAndUpsert(ctx, oc, cc, coll.ID, cfg.Embedder, chunks, cfg.BatchSize, cfg.Verbose)
		if err != nil {
			return fmt.Errorf("index %s: %w", collName, err)
		}
		stats.BatchesSent += batches
		stats.PerCollection[collName] = ups
		if isCommit {
			stats.CommitUpserted += ups
		} else {
			stats.SnapshotUpserted += ups
		}
	}
	return nil
}

// embedAndUpsert is the per-collection embed → upsert loop. Mirrors
// internal/repos.indexBatched: batched embed for throughput, per-
// chunk retry on batch failure, skip-and-warn on individual failures
// so one bad chunk never blocks the whole run.
func embedAndUpsert(
	ctx context.Context,
	oc *ollama.Client,
	cc *chroma.Client,
	collectionID, embedder string,
	chunks []corpus.Chunk,
	batchSize int,
	verbose bool,
) (upserted, batches int, err error) {
	const embedderMaxChars = 3500

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
			fmt.Fprintf(os.Stderr, "    warn: batch %d..%d failed (%v); retrying per-chunk\n", start, end, eerr)
			emb = make([][]float32, len(batch))
			for i, in := range inputs {
				single, ierr := oc.Embed(ctx, embedder, []string{in})
				if ierr != nil || len(single) != 1 {
					fmt.Fprintf(os.Stderr, "    skip chunk %s: %v\n", batch[i].ChunkID[:12], ierr)
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
			return upserted, batches, fmt.Errorf("upsert %d..%d: %w", start, end, err)
		}
		batches++
		upserted += len(ids)
		if verbose && (batches%10 == 0 || end == len(chunks)) {
			fmt.Fprintf(os.Stderr, "    progress: %d/%d (batch %d)\n", upserted, len(chunks), batches)
		}
	}
	return upserted, batches, nil
}

// groupChunksByScope walks the record list and produces two maps:
// scope → []chunk for commit summaries and file snapshots.
func groupChunksByScope(records []*CommitRecord) (commits, snapshots map[string][]corpus.Chunk) {
	commits = map[string][]corpus.Chunk{}
	snapshots = map[string][]corpus.Chunk{}
	for _, rec := range records {
		commits[rec.Scope] = append(commits[rec.Scope], buildCommitChunk(rec))
		for _, snap := range rec.Snapshots {
			snapshots[rec.Scope] = append(snapshots[rec.Scope], buildSnapshotChunk(rec, snap))
		}
	}
	// Deterministic ordering inside each scope for stable upserts.
	for s := range commits {
		sort.Slice(commits[s], func(i, j int) bool { return commits[s][i].ChunkID < commits[s][j].ChunkID })
	}
	for s := range snapshots {
		sort.Slice(snapshots[s], func(i, j int) bool { return snapshots[s][i].ChunkID < snapshots[s][j].ChunkID })
	}
	return commits, snapshots
}

// buildCommitChunk converts a CommitRecord into the commit-summary
// chunk indexed under history_<scope>. Body is the embedder input —
// subject + body + files-touched list (capped). Metadata carries
// everything the retriever might filter on later.
func buildCommitChunk(rec *CommitRecord) corpus.Chunk {
	var b strings.Builder
	b.WriteString(rec.Subject)
	if rec.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(rec.Body)
	}
	if len(rec.Files) > 0 {
		b.WriteString("\n\nFiles touched:")
		// Sort files by changed lines descending, cap at 10 for
		// embedding context.
		ranked := make([]FileTouch, len(rec.Files))
		copy(ranked, rec.Files)
		sort.SliceStable(ranked, func(i, j int) bool {
			ci := ranked[i].Additions + ranked[i].Deletions
			cj := ranked[j].Additions + ranked[j].Deletions
			return ci > cj
		})
		if len(ranked) > 10 {
			ranked = ranked[:10]
		}
		for _, f := range ranked {
			fmt.Fprintf(&b, "\n- %s (+%d/-%d)", f.Path, f.Additions, f.Deletions)
		}
	}
	id := chunkID("hist", rec.Repo+"@"+rec.SHA)
	return corpus.Chunk{
		ChunkID:    id,
		Source:     rec.Repo + "@" + rec.ShortSHA,
		SourceSHA:  rec.SHA,
		Scope:      rec.Scope,
		DocType:    "commit-summary",
		ArtifactID: rec.ShortSHA,
		Title:      rec.Subject,
		Body:       b.String(),
		Metadata: corpus.ChunkMetadata{
			Section:       "commit",
			CitesOutbound: rec.CiteIDs,
			TechTags:      []string{rec.Repo},
		},
		EmbeddedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// buildSnapshotChunk converts a (CommitRecord, FileSnapshot) pair
// into the file-snapshot chunk indexed under code_history_<scope>.
// Body is the file content as it existed at the commit — the
// retriever can answer "what did file X look like at commit Y?"
// directly from this.
func buildSnapshotChunk(rec *CommitRecord, snap FileSnapshot) corpus.Chunk {
	id := chunkID("snap", rec.Repo+"@"+rec.SHA+":"+snap.Path)
	body := snap.Content
	if body == "" {
		// Deleted files keep their pre-deletion content via the
		// parser; if the parser couldn't read either tree, fall
		// back to a stub so the embedder doesn't choke on empty.
		body = "[no content captured for " + snap.Path + " at " + rec.ShortSHA + "]"
	}
	return corpus.Chunk{
		ChunkID:      id,
		Source:       rec.Repo + "@" + rec.ShortSHA + ":" + snap.Path,
		SourceSHA:    rec.SHA,
		SourceAnchor: snap.Path,
		Scope:        rec.Scope,
		DocType:      "file-snapshot",
		ArtifactID:   rec.ShortSHA,
		Title:        snap.Path,
		Body:         body,
		Metadata: corpus.ChunkMetadata{
			Section:       snap.Status,
			CitesOutbound: rec.CiteIDs,
			TechTags:      []string{rec.Repo, snap.Path},
		},
		EmbeddedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// chunkID returns a deterministic 24-hex-char chunk ID for the
// given prefix + identifying key. Re-running Index on identical
// (rec, snap) pairs produces identical IDs, so ChromaDB upserts
// idempotently rather than duplicating rows.
func chunkID(prefix, key string) string {
	h := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(h[:12])
}

// chunkMetadataForChroma flattens a corpus.Chunk into the scalar/
// list metadata shape ChromaDB accepts. Mirrors the identical
// helper in internal/repos for now; future shared package would
// dedupe.
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
	if c.ArtifactID != "" {
		m["artifact_id"] = c.ArtifactID
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

// capChars trims s to maxChars at a UTF-8 boundary. Local copy
// rather than importing internal/repos — the function is 6 lines
// and the import would be cyclic-ish through corpus.
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
