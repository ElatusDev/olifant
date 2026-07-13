// Incremental repo sync (olifant#82, D-FF2): the D228 transplant for the
// code_* family. Change set = diff of the previously-landed repo manifest
// against a fresh walk; deletes key on chunk `source` metadata from the OLD
// scope's collection; manifest written LAST so an interrupted run re-diffs
// and repairs. A builder-version mismatch refuses to the drop-and-rebuild
// recovery path (a wrong-grain incremental corrupts silently).
package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// codeCollectionName mirrors the ingester's naming (ingester.go EnsureCollection).
func codeCollectionName(scope string) string {
	return "code_" + strings.ReplaceAll(scope, "-", "_")
}

// missingRepoDirs guards the manifest-bearing paths: a transiently-absent
// repo dir (an unmounted /Volumes, a moved checkout) must refuse, not be
// diffed as "removed" — that would mass-delete the repo's entire family
// content on the next sync.
func missingRepoDirs(specs []RepoSpec) []string {
	var missing []string
	for _, rs := range specs {
		if _, err := os.Stat(rs.Path); err != nil {
			missing = append(missing, rs.Name)
		}
	}
	return missing
}

// Sync performs one incremental re-index of the code_* family against the
// previously-landed repo manifest.
func Sync(ctx context.Context, cfg IngestConfig) (*corpus.SyncReport, error) {
	start := time.Now()
	if cfg.ManifestPath == "" {
		return nil, fmt.Errorf("repo sync: no manifest path configured")
	}

	old, err := corpus.LoadManifest(cfg.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("repo sync: no previously-landed manifest at %s (%w) — run the full `repo ingest` first (it mints the manifest, olifant#82 D-FF1)", cfg.ManifestPath, err)
	}

	if missing := missingRepoDirs(cfg.Repos); len(missing) > 0 {
		return nil, fmt.Errorf("repo sync: repo dir(s) missing: %s — refusing (an absent checkout would diff as wholesale removal and mass-delete its chunks); remount/restore the repo or re-mint via full ingest", strings.Join(missing, ", "))
	}

	scoped, _, err := CollectChunks(cfg)
	if err != nil {
		return nil, fmt.Errorf("repo sync: walk: %w", err)
	}
	fresh := BuildManifest(scoped)

	if old.BuilderVersion != fresh.BuilderVersion {
		return nil, fmt.Errorf("repo sync: builder version drift (%s indexed vs %s current) — a source diff cannot express a chunking change; run the full drop-and-rebuild (AP179 recipe)", old.BuilderVersion, fresh.BuilderVersion)
	}

	diff := corpus.DiffManifests(old, fresh)
	rep := &corpus.SyncReport{Added: len(diff.Added), Changed: len(diff.Changed), Removed: len(diff.Removed)}
	if diff.Empty() {
		rep.NoOp = true
		rep.ElapsedMs = time.Since(start).Milliseconds()
		return rep, nil // no writes at all: receipts stay fresh
	}
	if cfg.DryRun {
		rep.ElapsedMs = time.Since(start).Milliseconds()
		return rep, nil
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)
	if err := cc.EnsureTenant(ctx); err != nil {
		return nil, fmt.Errorf("repo sync: chroma EnsureTenant: %w", err)
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		return nil, fmt.Errorf("repo sync: chroma EnsureDatabase: %w", err)
	}

	collIDs := map[string]string{}
	collFor := func(scope string) (string, error) {
		if id, ok := collIDs[scope]; ok {
			return id, nil
		}
		coll, err := cc.EnsureCollection(ctx, codeCollectionName(scope), map[string]interface{}{
			"hnsw:space": "cosine", "olifant_scope": scope, "olifant_kind": "code",
		})
		if err != nil {
			return "", err
		}
		collIDs[scope] = coll.ID
		return coll.ID, nil
	}

	// Delete stale chunks: changed sources (old copy) + removed sources,
	// each from its OLD scope's collection.
	deleteFrom := func(s corpus.SourceManifest) error {
		id, err := collFor(s.Scope)
		if err != nil {
			return err
		}
		return cc.DeleteWhere(ctx, id, map[string]interface{}{"source": s.Path})
	}
	for _, c := range diff.Changed {
		if err := deleteFrom(c.Old); err != nil {
			return nil, fmt.Errorf("repo sync: delete %s: %w", c.Old.Path, err)
		}
	}
	for _, r := range diff.Removed {
		if err := deleteFrom(r); err != nil {
			return nil, fmt.Errorf("repo sync: delete %s: %w", r.Path, err)
		}
	}

	// Embed + upsert fresh chunks of added + changed sources, per scope.
	want := make(map[string]bool, len(diff.Added)+len(diff.Changed))
	for _, a := range diff.Added {
		want[a.Path] = true
	}
	for _, c := range diff.Changed {
		want[c.New.Path] = true
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 32
	}
	for scope, chunks := range scoped {
		var todo []corpus.Chunk
		for _, ch := range chunks {
			if want[ch.Source] {
				todo = append(todo, ch)
			}
		}
		if len(todo) == 0 {
			continue
		}
		id, err := collFor(scope)
		if err != nil {
			return nil, fmt.Errorf("repo sync: collection %s: %w", scope, err)
		}
		up, _, err := indexBatched(ctx, oc, cc, id, cfg.Embedder, todo, batch, cfg.Verbose)
		if err != nil {
			return nil, fmt.Errorf("repo sync: index %s: %w", scope, err)
		}
		rep.ChunksEmbedded += up
	}

	// Writes LAST (D-FF2 = D-CS2): NDJSON (optional), then the manifest.
	if cfg.WriteNDJSON && cfg.OutDir != "" {
		for scope, chunks := range scoped {
			outPath := filepath.Join(cfg.OutDir, scope+".ndjson")
			if err := writeChunksNDJSON(outPath, chunks); err != nil {
				return nil, fmt.Errorf("repo sync: write %s: %w", outPath, err)
			}
		}
	}
	if err := corpus.WriteManifest(cfg.ManifestPath, fresh); err != nil {
		return nil, fmt.Errorf("repo sync: write manifest: %w", err)
	}
	rep.ElapsedMs = time.Since(start).Milliseconds()
	return rep, nil
}

// Status compares the landed repo manifest against a fresh walk — fully
// offline (no chroma, no ollama). The code-family half of the family-aware
// freshness observable (D-FF6).
func Status(cfg IngestConfig) (*corpus.StatusReport, error) {
	if cfg.ManifestPath == "" {
		return nil, fmt.Errorf("repo status: no manifest path configured")
	}
	old, err := corpus.LoadManifest(cfg.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("repo status: no landed manifest (%w) — run the full `repo ingest` first", err)
	}
	scoped, _, err := CollectChunks(cfg)
	if err != nil {
		return nil, fmt.Errorf("repo status: walk: %w", err)
	}
	fresh := BuildManifest(scoped)
	d := corpus.DiffManifests(old, fresh)
	return &corpus.StatusReport{
		BuiltAt:        old.BuiltAt,
		BuilderVersion: old.BuilderVersion,
		IndexedSources: len(old.Sources),
		IndexedChunks:  old.TotalChunks,
		Added:          len(d.Added),
		Changed:        len(d.Changed),
		Removed:        len(d.Removed),
		VersionDrift:   old.BuilderVersion != fresh.BuilderVersion,
	}, nil
}
