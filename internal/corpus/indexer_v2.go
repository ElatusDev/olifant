package corpus

import (
	"context"
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

// DefaultV2Collection is the Chroma collection name the RAG-pivot Phase A uses.
// Distinct from the v1 `corpus_<scope>` naming so v1 stays untouched.
const DefaultV2Collection = "olifant-v2-curriculum"

// IndexV2Config drives `olifant corpus index-v2`: walking the
// v2-curriculum YAMLs (vocab Symbols + prose Sentences), embedding via
// nomic-embed-text, and upserting into a single ChromaDB collection
// with tag fields preserved as metadata.
type IndexV2Config struct {
	KBRoot     string
	Collection string
	OllamaURL  string
	ChromaURL  string
	Embedder   string
	Tenant     string
	Database   string
	BatchSize  int
	OnlyKinds  []string // empty | "vocab" | "prose" — defaults to both
	Verbose    bool
	DryRun     bool
	Smoke      bool   // run smoke queries after upsert
	SmokeOut   string // write smoke report here when Smoke (optional)
}

// IndexV2Stats summarises one run.
type IndexV2Stats struct {
	VocabFilesRead int
	ProseFilesRead int
	SymbolsRead    int
	SentencesRead  int
	ChunksUpserted int
	BatchesSent    int
	Elapsed        time.Duration
	ByRepo         map[string]int
	ByKind         map[string]int
	Smoke          []SmokeResult
}

// SmokeResult holds the top-K hits for one canned smoke query.
type SmokeResult struct {
	Query string
	Hits  []SmokeHit
}

// SmokeHit is one returned chunk from a smoke query.
type SmokeHit struct {
	ID       string
	Distance float32
	Text     string
	Source   string
	Repo     string
	ItemKind string
}

// v2Item is a unified shape lifted from Symbol|Sentence with the
// derived repo / slice context. Embedding input is `Text`; everything
// else lands as Chroma metadata.
type v2Item struct {
	ID       string
	Text     string
	Source   string
	Line     int
	Tags     map[string]any
	ItemKind string // "symbol" | "sentence"
	Repo     string
	Slice    string // optional sub-slice (knowledge-base/dictionary)
}

// IndexV2 walks <KBRoot>/corpus/v2-curriculum/{vocab,prose}/**/*.yaml,
// embeds each entry via the configured Ollama embedder, and upserts
// into a single Chroma collection. Per-item tag fields land in
// collection metadata (multi-valued []string axes joined to comma
// strings to satisfy Chroma's scalar-only metadata constraint).
//
// The function is idempotent — Chroma's upsert overwrites on existing
// IDs (we prefix with item kind to keep symbols + sentences from
// colliding even if their hashes ever did).
func IndexV2(ctx context.Context, cfg IndexV2Config) (IndexV2Stats, error) {
	stats := IndexV2Stats{
		ByRepo: map[string]int{},
		ByKind: map[string]int{},
	}
	start := time.Now()

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Collection == "" {
		cfg.Collection = DefaultV2Collection
	}

	wantVocab, wantProse := selectKinds(cfg.OnlyKinds)

	root := filepath.Join(cfg.KBRoot, "corpus", "v2-curriculum")
	if _, err := os.Stat(root); err != nil {
		return stats, fmt.Errorf("v2-curriculum root %s: %w", root, err)
	}

	var items []v2Item
	if wantVocab {
		vItems, vFiles, err := loadVocabItems(filepath.Join(root, "vocab"))
		if err != nil {
			return stats, fmt.Errorf("load vocab: %w", err)
		}
		stats.VocabFilesRead = vFiles
		stats.SymbolsRead = len(vItems)
		items = append(items, vItems...)
	}
	if wantProse {
		pItems, pFiles, err := loadProseItems(filepath.Join(root, "prose"))
		if err != nil {
			return stats, fmt.Errorf("load prose: %w", err)
		}
		stats.ProseFilesRead = pFiles
		stats.SentencesRead = len(pItems)
		items = append(items, pItems...)
	}

	for _, it := range items {
		stats.ByRepo[it.Repo]++
		stats.ByKind[it.ItemKind]++
	}

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr,
			"v2 corpus: %d vocab files, %d prose files, %d symbols, %d sentences (%d total items)\n",
			stats.VocabFilesRead, stats.ProseFilesRead,
			stats.SymbolsRead, stats.SentencesRead, len(items))
	}

	if cfg.DryRun {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

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

	coll, err := cc.EnsureCollection(ctx, cfg.Collection, map[string]interface{}{
		"hnsw:space":     "cosine",
		"olifant_corpus": "v2-curriculum",
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return stats, fmt.Errorf("EnsureCollection %s: %w", cfg.Collection, err)
	}
	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "collection: %s (id=%s)\n", coll.Name, coll.ID)
	}

	upserted, batches, err := indexV2Items(ctx, oc, cc, coll.ID, cfg.Embedder, items, cfg.BatchSize, cfg.Verbose)
	if err != nil {
		return stats, fmt.Errorf("upsert: %w", err)
	}
	stats.ChunksUpserted = upserted
	stats.BatchesSent = batches

	if cfg.Smoke {
		results, err := runSmoke(ctx, oc, cc, coll.ID, cfg.Embedder)
		if err != nil {
			return stats, fmt.Errorf("smoke: %w", err)
		}
		stats.Smoke = results
		if cfg.SmokeOut != "" {
			if err := writeSmokeReport(cfg.SmokeOut, cfg.Collection, results); err != nil {
				return stats, fmt.Errorf("write smoke report: %w", err)
			}
		}
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

func selectKinds(only []string) (vocab, prose bool) {
	if len(only) == 0 {
		return true, true
	}
	for _, k := range only {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "vocab":
			vocab = true
		case "prose":
			prose = true
		}
	}
	return vocab, prose
}

// loadVocabItems walks vocab/<repo>/**/*.yaml, parses each as []Symbol,
// and lifts into v2Items tagged with ItemKind="symbol" + derived Repo.
func loadVocabItems(vocabRoot string) ([]v2Item, int, error) {
	var items []v2Item
	files, err := walkYAMLs(vocabRoot)
	if err != nil {
		return nil, 0, err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", f, err)
		}
		var syms []Symbol
		if err := yaml.Unmarshal(raw, &syms); err != nil {
			return nil, 0, fmt.Errorf("parse %s: %w", f, err)
		}
		repo, slice := deriveVocabRepoSlice(vocabRoot, f)
		for _, s := range syms {
			items = append(items, v2Item{
				ID:       s.ID,
				Text:     s.Text,
				Source:   s.Source,
				Line:     s.Line,
				Tags:     s.Tags,
				ItemKind: "symbol",
				Repo:     repo,
				Slice:    slice,
			})
		}
	}
	return items, len(files), nil
}

// loadProseItems walks prose/**/*.yaml, parses each as []Sentence, and
// lifts into v2Items tagged with ItemKind="sentence" + derived Repo.
func loadProseItems(proseRoot string) ([]v2Item, int, error) {
	var items []v2Item
	files, err := walkYAMLs(proseRoot)
	if err != nil {
		return nil, 0, err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", f, err)
		}
		var sents []Sentence
		if err := yaml.Unmarshal(raw, &sents); err != nil {
			return nil, 0, fmt.Errorf("parse %s: %w", f, err)
		}
		repo, slice := deriveProseRepoSlice(proseRoot, f)
		for _, s := range sents {
			items = append(items, v2Item{
				ID:       s.ID,
				Text:     s.Text,
				Source:   s.Source,
				Line:     s.Line,
				Tags:     s.Tags,
				ItemKind: "sentence",
				Repo:     repo,
				Slice:    slice,
			})
		}
	}
	return items, len(files), nil
}

func walkYAMLs(root string) ([]string, error) {
	var out []string
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".yaml") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// deriveVocabRepoSlice maps vocab/<repo>/<rest>.yaml → (repo, rest-without-ext).
func deriveVocabRepoSlice(vocabRoot, file string) (repo, slice string) {
	rel, err := filepath.Rel(vocabRoot, file)
	if err != nil {
		return "", strings.TrimSuffix(filepath.Base(file), ".yaml")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 {
		return "", strings.TrimSuffix(filepath.Base(file), ".yaml")
	}
	repo = parts[0]
	if len(parts) == 1 {
		return repo, ""
	}
	tail := strings.Join(parts[1:], "/")
	slice = strings.TrimSuffix(tail, ".yaml")
	return repo, slice
}

// deriveProseRepoSlice maps:
//   - prose/<repo>.yaml             → (repo, "")
//   - prose/<repo>/<slice>.yaml     → (repo, slice)
func deriveProseRepoSlice(proseRoot, file string) (repo, slice string) {
	rel, err := filepath.Rel(proseRoot, file)
	if err != nil {
		return "", strings.TrimSuffix(filepath.Base(file), ".yaml")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 1 {
		return strings.TrimSuffix(parts[0], ".yaml"), ""
	}
	repo = parts[0]
	tail := strings.Join(parts[1:], "/")
	slice = strings.TrimSuffix(tail, ".yaml")
	return repo, slice
}

// indexV2Items batches items through embed → upsert. Mirrors the v1
// indexer's per-batch fallback (one bad item shouldn't kill the batch).
func indexV2Items(
	ctx context.Context, oc *ollama.Client, cc *chroma.Client,
	collectionID, embedder string, items []v2Item, batchSize int, verbose bool,
) (upserted, batches int, err error) {
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[start:end]

		inputs := make([]string, len(batch))
		for i, it := range batch {
			inputs[i] = capInput(it.Text, embedderMaxChars)
		}

		emb, err := oc.Embed(ctx, embedder, inputs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    warn: batch %d..%d failed (%v); retrying per-item\n",
				start, end, err)
			emb = make([][]float32, len(batch))
			skipped := 0
			for i, in := range inputs {
				single, ierr := oc.Embed(ctx, embedder, []string{in})
				if ierr != nil || len(single) != 1 {
					fmt.Fprintf(os.Stderr, "    skip item %s (%d chars): %v\n",
						truncID(batch[i].ID), len(in), ierr)
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

		ids := make([]string, 0, len(batch))
		docs := make([]string, 0, len(batch))
		metas := make([]map[string]interface{}, 0, len(batch))
		filtered := make([][]float32, 0, len(batch))
		for i, it := range batch {
			if emb[i] == nil {
				continue
			}
			ids = append(ids, namespacedID(it))
			docs = append(docs, it.Text)
			metas = append(metas, v2MetadataForChroma(it))
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
		if verbose && (batches%10 == 0 || end == len(items)) {
			fmt.Fprintf(os.Stderr, "    progress: %d/%d (batch %d)\n", upserted, len(items), batches)
		}
	}
	return upserted, batches, nil
}

func truncID(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16]
}

// namespacedID prefixes the raw Symbol/Sentence ID with its item kind
// so symbol/sentence IDs share no namespace even if their hashes ever
// collided.
func namespacedID(it v2Item) string {
	return it.ItemKind + "-" + it.ID
}

// v2MetadataForChroma lifts an item's repo/source/tags into the
// scalar-only metadata shape Chroma expects. Multi-valued []string
// axes (concern, subject_ref) are comma-joined.
func v2MetadataForChroma(it v2Item) map[string]interface{} {
	m := map[string]interface{}{
		"item_kind": it.ItemKind,
		"source":    it.Source,
		"line":      it.Line,
	}
	if it.Repo != "" {
		m["repo"] = it.Repo
	}
	if it.Slice != "" {
		m["slice"] = it.Slice
	}
	if it.ID != "" {
		m["item_id"] = it.ID
	}
	for k, v := range it.Tags {
		if k == "" || v == nil {
			continue
		}
		switch tv := v.(type) {
		case string:
			if tv != "" {
				m[k] = tv
			}
		case []string:
			if len(tv) > 0 {
				m[k] = strings.Join(tv, ",")
			}
		case []any:
			parts := make([]string, 0, len(tv))
			for _, e := range tv {
				if s, ok := e.(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				m[k] = strings.Join(parts, ",")
			}
		case int:
			m[k] = tv
		case bool:
			m[k] = tv
		}
	}
	return m
}

// smokeQueries — the 5 canned queries the prompt §4 Phase A1 verification calls for.
// Each targets a different scope/concern so a healthy index should retrieve
// distinct top-K chunks.
var smokeQueries = []string{
	"What is the canonical Java package root used across core-api?",
	"AP86 immutable Domain object thread safety prototype scope",
	"RTK Query baseApi injectEndpoints feature isolation webapp",
	"tenant-scoped data filter Hibernate composite key multi-tenant",
	"Expo SecureStore biometrics FaceID Keychain Android KeyStore",
}

// runSmoke embeds each canned query and pulls the top-5 hits.
func runSmoke(
	ctx context.Context, oc *ollama.Client, cc *chroma.Client,
	collectionID, embedder string,
) ([]SmokeResult, error) {
	out := make([]SmokeResult, 0, len(smokeQueries))
	for _, q := range smokeQueries {
		emb, err := oc.Embed(ctx, embedder, []string{q})
		if err != nil {
			return nil, fmt.Errorf("embed query %q: %w", q, err)
		}
		if len(emb) != 1 {
			return nil, fmt.Errorf("embed query %q: expected 1 vector, got %d", q, len(emb))
		}
		resp, err := cc.Query(ctx, collectionID, chroma.QueryRequest{
			QueryEmbeddings: [][]float32{emb[0]},
			NResults:        5,
			Include:         []string{"documents", "metadatas", "distances"},
		})
		if err != nil {
			return nil, fmt.Errorf("query %q: %w", q, err)
		}
		hits := make([]SmokeHit, 0, 5)
		if len(resp.IDs) > 0 {
			ids := resp.IDs[0]
			docs := safeStringSlice(resp.Documents, 0)
			metas := safeMetaSlice(resp.Metadatas, 0)
			dists := safeFloatSlice(resp.Distances, 0)
			for i, id := range ids {
				h := SmokeHit{ID: id}
				if i < len(docs) {
					h.Text = docs[i]
				}
				if i < len(dists) {
					h.Distance = dists[i]
				}
				if i < len(metas) {
					if v, ok := metas[i]["source"].(string); ok {
						h.Source = v
					}
					if v, ok := metas[i]["repo"].(string); ok {
						h.Repo = v
					}
					if v, ok := metas[i]["item_kind"].(string); ok {
						h.ItemKind = v
					}
				}
				hits = append(hits, h)
			}
		}
		out = append(out, SmokeResult{Query: q, Hits: hits})
	}
	return out, nil
}

func safeStringSlice(s [][]string, i int) []string {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func safeMetaSlice(s [][]map[string]interface{}, i int) []map[string]interface{} {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func safeFloatSlice(s [][]float32, i int) []float32 {
	if i < len(s) {
		return s[i]
	}
	return nil
}

// writeSmokeReport renders the smoke results as a markdown table at `path`.
// Caller owns directory creation if needed.
func writeSmokeReport(path, collection string, results []SmokeResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Phase A1 smoke — collection `%s`\n\n", collection)
	fmt.Fprintf(&b, "Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	for i, r := range results {
		fmt.Fprintf(&b, "## Q%d: %s\n\n", i+1, r.Query)
		if len(r.Hits) == 0 {
			b.WriteString("_(no hits)_\n\n")
			continue
		}
		b.WriteString("| # | distance | repo | kind | source | text |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for j, h := range r.Hits {
			text := strings.ReplaceAll(h.Text, "|", "\\|")
			text = strings.ReplaceAll(text, "\n", " ")
			if len(text) > 120 {
				text = text[:120] + "…"
			}
			fmt.Fprintf(&b, "| %d | %.4f | %s | %s | %s | %s |\n",
				j+1, h.Distance, h.Repo, h.ItemKind, h.Source, text)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
