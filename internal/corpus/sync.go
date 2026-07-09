// Incremental corpus sync (olifant#77, D-CS1..7): implements the CORPUS-V1
// §Rebuild-triggers promise — re-embed only what changed. The change set is a
// diff of the previously-landed manifest against a fresh in-memory build;
// deletes key on chunk `source` metadata (chroma.DeleteWhere — the primitive
// whose absence was the AP179 orphan root). The manifest and NDJSON are
// written LAST, so an interrupted run re-diffs the same sources and repairs
// itself. A builder-version mismatch refuses to the full-rebuild recovery
// path: a wrong-grain incremental would corrupt the index silently.
package corpus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// SyncConfig drives one incremental sync run.
type SyncConfig struct {
	Config    // the builder config (KBRoot, PlatformRoot, MemoryRoot, OutDir, Verbose)
	OllamaURL string
	ChromaURL string
	Embedder  string
	Tenant    string
	Database  string
	BatchSize int
	DryRun    bool // diff + report only; no deletes, embeds, or writes
}

// SyncReport summarizes one sync run.
type SyncReport struct {
	NoOp           bool
	Added          int
	Changed        int
	Removed        int
	ChunksEmbedded int
	ElapsedMs      int64
}

// ManifestDiff is the change set between two manifests, keyed by source path.
// Changed carries the OLD entry too: a scope move must delete from the old
// scope's collection while the new chunks land in the new scope's.
type ManifestDiff struct {
	Added   []SourceManifest
	Changed []struct{ Old, New SourceManifest }
	Removed []SourceManifest
}

// Empty reports whether the diff carries no work.
func (d ManifestDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Changed) == 0 && len(d.Removed) == 0
}

// LoadManifest reads a corpus manifest.yaml.
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// DiffManifests computes the source-level change set old → new. BuiltAt is
// ignored (it changes every build); identity is (path), change is SHA or
// scope drift.
func DiffManifests(old, new Manifest) ManifestDiff {
	oldByPath := make(map[string]SourceManifest, len(old.Sources))
	for _, s := range old.Sources {
		oldByPath[s.Path] = s
	}
	var d ManifestDiff
	seen := make(map[string]bool, len(new.Sources))
	for _, n := range new.Sources {
		seen[n.Path] = true
		o, ok := oldByPath[n.Path]
		switch {
		case !ok:
			d.Added = append(d.Added, n)
		case o.SHA != n.SHA || o.Scope != n.Scope:
			d.Changed = append(d.Changed, struct{ Old, New SourceManifest }{o, n})
		}
	}
	for _, o := range old.Sources {
		if !seen[o.Path] {
			d.Removed = append(d.Removed, o)
		}
	}
	return d
}

// Sync performs one incremental re-index against the target tree's
// previously-landed manifest.
func Sync(ctx context.Context, cfg SyncConfig) (*SyncReport, error) {
	start := time.Now()
	manifestPath := filepath.Join(cfg.OutDir, "manifest.yaml")

	old, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("sync: no previously-landed manifest at %s (%w) — run the full rebuild first (AP179 recipe)", manifestPath, err)
	}

	scoped, fresh, err := buildCorpus(cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("sync: build: %w", err)
	}

	// D-CS3: a chunker/schema change alters bodies of unchanged sources —
	// not source-diffable. Refuse loudly to the recovery path.
	if old.BuilderVersion != fresh.BuilderVersion {
		return nil, fmt.Errorf("sync: builder version drift (%s indexed vs %s current) — a source diff cannot express a chunking change; run the full drop-and-rebuild (AP179 recipe)", old.BuilderVersion, fresh.BuilderVersion)
	}

	diff := DiffManifests(old, fresh)
	rep := &SyncReport{Added: len(diff.Added), Changed: len(diff.Changed), Removed: len(diff.Removed)}
	if diff.Empty() {
		rep.NoOp = true
		rep.ElapsedMs = time.Since(start).Milliseconds()
		return rep, nil // no writes at all: receipts stay fresh (AC8)
	}
	if cfg.DryRun {
		rep.ElapsedMs = time.Since(start).Milliseconds()
		return rep, nil
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)
	if err := cc.EnsureTenant(ctx); err != nil {
		return nil, fmt.Errorf("sync: chroma EnsureTenant: %w", err)
	}
	if err := cc.EnsureDatabase(ctx); err != nil {
		return nil, fmt.Errorf("sync: chroma EnsureDatabase: %w", err)
	}

	collIDs := map[string]string{} // scope → collection id (lazily ensured)
	collFor := func(scope string) (string, error) {
		if id, ok := collIDs[scope]; ok {
			return id, nil
		}
		coll, err := cc.EnsureCollection(ctx, collectionName(scope), map[string]interface{}{
			"hnsw:space": "cosine", "olifant_scope": scope,
		})
		if err != nil {
			return "", err
		}
		collIDs[scope] = coll.ID
		return coll.ID, nil
	}

	// Delete stale chunks: changed sources (old copy) + removed sources,
	// each from its OLD scope's collection.
	deleteFrom := func(s SourceManifest) error {
		id, err := collFor(s.Scope)
		if err != nil {
			return err
		}
		return cc.DeleteWhere(ctx, id, map[string]interface{}{"source": s.Path})
	}
	for _, c := range diff.Changed {
		if err := deleteFrom(c.Old); err != nil {
			return nil, fmt.Errorf("sync: delete %s: %w", c.Old.Path, err)
		}
	}
	for _, r := range diff.Removed {
		if err := deleteFrom(r); err != nil {
			return nil, fmt.Errorf("sync: delete %s: %w", r.Path, err)
		}
	}

	// Embed + upsert the fresh chunks of added + changed sources, grouped
	// per scope so indexScope batches efficiently.
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
	for _, scope := range AllScopes {
		var todo []Chunk
		for _, ch := range scoped[scope] {
			if want[ch.Source] {
				todo = append(todo, ch)
			}
		}
		if len(todo) == 0 {
			continue
		}
		id, err := collFor(scope)
		if err != nil {
			return nil, fmt.Errorf("sync: collection %s: %w", scope, err)
		}
		up, _, err := indexScope(ctx, oc, cc, id, cfg.Embedder, todo, batch, cfg.Verbose)
		if err != nil {
			return nil, fmt.Errorf("sync: index %s: %w", scope, err)
		}
		rep.ChunksEmbedded += up
	}

	// Writes LAST (D-CS2): an interrupted run above leaves the old manifest
	// in place, so the next sync re-diffs the same sources and repairs.
	for _, scope := range AllScopes {
		outPath := filepath.Join(cfg.OutDir, scope+".ndjson")
		if err := writeNDJSON(outPath, scoped[scope]); err != nil {
			return nil, fmt.Errorf("sync: write %s: %w", outPath, err)
		}
	}
	if err := writeManifest(manifestPath, fresh); err != nil {
		return nil, fmt.Errorf("sync: write manifest: %w", err)
	}
	rep.ElapsedMs = time.Since(start).Milliseconds()
	return rep, nil
}

// StatusReport is the freshness observable (olifant#77, D-CS6).
type StatusReport struct {
	BuiltAt        string
	BuilderVersion string
	IndexedSources int
	IndexedChunks  int
	Added          int
	Changed        int
	Removed        int
	VersionDrift   bool
}

// Status compares the landed manifest against a fresh in-memory build —
// fully offline (no chroma, no ollama).
func Status(cfg Config) (*StatusReport, error) {
	old, err := LoadManifest(filepath.Join(cfg.OutDir, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("status: no landed manifest (%w) — run the full rebuild first", err)
	}
	_, fresh, err := buildCorpus(cfg)
	if err != nil {
		return nil, fmt.Errorf("status: build: %w", err)
	}
	d := DiffManifests(old, fresh)
	return &StatusReport{
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

// LiveCounts returns per-scope chroma collection counts (the optional live
// half of status).
func LiveCounts(ctx context.Context, chromaURL, tenant, database string) (map[string]int64, error) {
	cc := chroma.New(chromaURL, tenant, database)
	out := make(map[string]int64, len(AllScopes))
	for _, scope := range AllScopes {
		coll, err := cc.EnsureCollection(ctx, collectionName(scope), nil)
		if err != nil {
			return nil, err
		}
		n, err := cc.Count(ctx, coll.ID)
		if err != nil {
			return nil, err
		}
		out[scope] = n
	}
	return out, nil
}
