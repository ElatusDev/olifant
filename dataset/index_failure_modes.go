package dataset

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
	"gopkg.in/yaml.v3"
)

// IndexConfig drives IndexFailureModes — the ChromaDB-side flow that
// makes failure-mode corrections retrievable at challenge time.
// Mirrors history.IndexConfig in shape.
type IndexConfig struct {
	KBRoot       string
	OllamaURL    string
	ChromaURL    string
	ChromaTenant string
	ChromaDB     string
	Embedder     string
	BatchSize    int
	Verbose      bool
	DryRun       bool
}

// IndexStats summarizes one IndexFailureModes call.
type IndexStats struct {
	EntriesRead     int
	Chunks          int
	Upserted        int
	BatchesSent     int
	PerCollection   map[string]int // collection name → upsert count
	Elapsed         time.Duration
}

const (
	failureModesCollectionPrefix = "failure_modes"
	embedderMaxChars             = 8000 // mirror history's cap
)

// IndexFailureModes loads <kbRoot>/eval/failure-modes/v<N>.yaml, builds
// one chunk per entry, embeds via Ollama, and upserts to
// failure_modes_<scope> ChromaDB collections. Idempotent — re-running
// against the same source is a no-op upsert.
//
// Each chunk's body intentionally bundles user_prompt + correct
// response + rationale. The rationale typically names BOTH the
// correct form and the wrong form (e.g. "use com.akademiaplus not
// com.akademia"), so semantic similarity surfaces the chunk when
// either form appears in the query — which is exactly what we want
// for retrieval-time correction.
func IndexFailureModes(ctx context.Context, cfg IndexConfig) (*IndexStats, error) {
	stats := &IndexStats{PerCollection: map[string]int{}}
	start := time.Now()

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Embedder == "" {
		cfg.Embedder = "nomic-embed-text"
	}

	// 1. Load entries from the latest curated source file.
	dir := filepath.Join(cfg.KBRoot, failureModesDir)
	path, err := pickLatestFailureModesFile(dir)
	if err != nil {
		if os.IsNotExist(err) {
			stats.Elapsed = time.Since(start)
			return stats, nil
		}
		return stats, err
	}
	if path == "" {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}
	entries, err := loadFailureModeEntries(path)
	if err != nil {
		return stats, err
	}
	stats.EntriesRead = len(entries)

	// 2. Build per-scope chunks.
	relPath, _ := filepath.Rel(cfg.KBRoot, path)
	relPath = filepath.ToSlash(relPath)
	chunksByScope := groupFailureModesByScope(entries, relPath)
	for _, cs := range chunksByScope {
		stats.Chunks += len(cs)
	}

	if cfg.DryRun {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}

	// 3. Wire up Chroma + Ollama and ensure tenant/db exist (mirrors
	//    history.Index pre-flight).
	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.ChromaTenant, cfg.ChromaDB)
	if _, err := oc.Version(ctx); err != nil {
		return stats, fmt.Errorf("IndexFailureModes: ollama unreachable: %w", err)
	}
	if _, err := cc.Heartbeat(ctx); err != nil {
		return stats, fmt.Errorf("IndexFailureModes: chroma unreachable: %w", err)
	}
	if err := cc.EnsureTenant(ctx); err != nil {
		return stats, err
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		return stats, err
	}

	// 4. Upsert per-scope.
	scopes := make([]string, 0, len(chunksByScope))
	for s := range chunksByScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	for _, scope := range scopes {
		chunks := chunksByScope[scope]
		collName := failureModesCollectionPrefix + "_" + strings.ReplaceAll(scope, "-", "_")
		coll, err := cc.EnsureCollection(ctx, collName, map[string]interface{}{
			"hnsw:space":    "cosine",
			"olifant_scope": scope,
			"olifant_kind":  "failure-mode-correction",
			"created_at":    time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return stats, fmt.Errorf("EnsureCollection %s: %w", collName, err)
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  upserting %d chunks → %s (id=%s)\n", len(chunks), collName, coll.ID)
		}
		ups, batches, err := embedAndUpsertFM(ctx, oc, cc, coll.ID, cfg.Embedder, chunks, cfg.BatchSize)
		if err != nil {
			return stats, fmt.Errorf("upsert %s: %w", collName, err)
		}
		stats.Upserted += ups
		stats.BatchesSent += batches
		stats.PerCollection[collName] = ups
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

// failureModeChunk is the embed-time representation of one entry.
// Decoupled from corpus.Chunk so this package stays independent of
// the corpus package's NDJSON pipeline.
type failureModeChunk struct {
	ID       string
	Body     string
	Metadata map[string]interface{}
}

// groupFailureModesByScope walks the loaded entries and produces one
// failureModeChunk per entry, bucketed by scope. Source path is
// captured in metadata for citation back to v1.yaml.
func groupFailureModesByScope(entries []failureModeEntry, relPath string) map[string][]failureModeChunk {
	out := map[string][]failureModeChunk{}
	for _, e := range entries {
		// Match the extractor's skip rule (extract_failure_modes.go):
		// drop entries lacking either the question or the answer.
		// Rationale-only entries carry no Q→A retrieval signal.
		if strings.TrimSpace(e.UserPrompt) == "" || strings.TrimSpace(e.CorrectAssistantResponse) == "" {
			continue
		}
		scope := strings.TrimSpace(e.Scope)
		if scope == "" {
			scope = "universal"
		}
		body := composeFailureModeBody(e)
		if strings.TrimSpace(body) == "" {
			continue
		}
		meta := map[string]interface{}{
			"failure_mode_id": strings.TrimSpace(e.ID),
			"code":            strings.TrimSpace(e.Code),
			"scope":           scope,
			"source":          relPath + "#" + strings.TrimSpace(e.ID),
		}
		if s := strings.TrimSpace(e.Cite); s != "" {
			meta["origin_cite"] = s
		}
		out[scope] = append(out[scope], failureModeChunk{
			ID:       chunkID("fm", strings.TrimSpace(e.ID)),
			Body:     body,
			Metadata: meta,
		})
	}
	return out
}

// composeFailureModeBody produces the text the embedder sees. We
// concatenate the user_prompt (so the embedder learns the kind of
// question this entry answers) + correct_assistant_response (the
// teaching content) + rationale (which typically names both the
// correct AND the wrong form, surfacing the chunk for either query
// shape).
func composeFailureModeBody(e failureModeEntry) string {
	var b strings.Builder
	if s := strings.TrimSpace(e.UserPrompt); s != "" {
		b.WriteString("Q: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(e.CorrectAssistantResponse); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("A: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(e.Rationale); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Rationale: ")
		b.WriteString(s)
	}
	return b.String()
}

// chunkID is duplicated from history/index.go's helper rather than
// imported to keep the dataset package's dependency graph clean.
func chunkID(prefix, key string) string {
	sum := sha1.Sum([]byte(prefix + ":" + key))
	return hex.EncodeToString(sum[:12])
}

// embedAndUpsertFM is the per-collection embed → upsert loop.
// Patterned after history/index.go's embedAndUpsert but specialized
// for failureModeChunk to keep package boundaries clean.
func embedAndUpsertFM(
	ctx context.Context,
	oc *ollama.Client,
	cc *chroma.Client,
	collectionID, embedder string,
	chunks []failureModeChunk,
	batchSize int,
) (upserted, batches int, err error) {
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
			return upserted, batches, fmt.Errorf("embed batch %d-%d: %w", start, end, eerr)
		}
		if len(emb) != len(batch) {
			return upserted, batches, fmt.Errorf("embed returned %d vectors for %d inputs", len(emb), len(batch))
		}

		ids := make([]string, len(batch))
		docs := make([]string, len(batch))
		metas := make([]map[string]interface{}, len(batch))
		for i, c := range batch {
			ids[i] = c.ID
			docs[i] = c.Body
			metas[i] = c.Metadata
		}
		if err := cc.Upsert(ctx, collectionID, chroma.UpsertRequest{
			IDs:        ids,
			Embeddings: emb,
			Documents:  docs,
			Metadatas:  metas,
		}); err != nil {
			return upserted, batches, fmt.Errorf("upsert batch %d-%d: %w", start, end, err)
		}
		upserted += len(batch)
		batches++
	}
	return upserted, batches, nil
}

// capChars caps embed input length so it never exceeds the
// embedder's context. Char-based (not token-based) since
// nomic-embed-text uses sentencepiece with ~3.5 chars/token average.
func capChars(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

// loadFailureModeEntries thinly wraps the YAML parse so callers can
// access the loaded entries without re-running the full extraction
// pipeline. Used by IndexFailureModes.
func loadFailureModeEntries(path string) ([]failureModeEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc failureModesYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc.Entries, nil
}
