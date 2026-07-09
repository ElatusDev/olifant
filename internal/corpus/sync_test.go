package corpus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkManifest(version string, sources ...SourceManifest) Manifest {
	return Manifest{BuiltAt: "2026-07-09T00:00:00Z", BuilderVersion: version, Sources: sources}
}

func src(path, sha, scope string) SourceManifest {
	return SourceManifest{Path: path, SHA: sha, Scope: scope, DocType: "doc", Chunks: 1}
}

func TestDiffManifests(t *testing.T) {
	old := mkManifest(BuilderVersion,
		src("a.md", "s1", "universal"),
		src("b.md", "s2", "backend"),
		src("gone.md", "s3", "webapp"),
		src("moved.md", "s4", "backend"),
	)
	fresh := mkManifest(BuilderVersion,
		src("a.md", "s1", "universal"),   // unchanged
		src("b.md", "s2-new", "backend"), // content change
		src("new.md", "s5", "mobile"),    // added
		src("moved.md", "s4", "e2e"),     // scope move, same content
	)
	d := DiffManifests(old, fresh)
	if len(d.Added) != 1 || d.Added[0].Path != "new.md" {
		t.Errorf("added = %+v, want new.md", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Path != "gone.md" {
		t.Errorf("removed = %+v, want gone.md", d.Removed)
	}
	if len(d.Changed) != 2 {
		t.Fatalf("changed = %+v, want b.md (sha) + moved.md (scope)", d.Changed)
	}
	// The scope move keeps the OLD scope on the old side — the delete must
	// target the old collection.
	for _, c := range d.Changed {
		if c.New.Path == "moved.md" && c.Old.Scope != "backend" {
			t.Errorf("scope move lost the old scope: %+v", c)
		}
	}
	// BuiltAt drift alone is not a change.
	same := mkManifest(BuilderVersion, src("a.md", "s1", "universal"))
	same2 := mkManifest(BuilderVersion, src("a.md", "s1", "universal"))
	same2.BuiltAt = "2027-01-01T00:00:00Z"
	if !DiffManifests(same, same2).Empty() {
		t.Error("BuiltAt-only drift must be a no-op diff")
	}
}

// syncFixture builds a minimal KB tree with one indexable doc plus a landed
// manifest in OutDir, returning a ready SyncConfig (no stack endpoints — the
// paths under test never reach chroma/ollama).
func syncFixture(t *testing.T) SyncConfig {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	out := filepath.Join(kb, "corpus", "v1")
	if err := os.MkdirAll(filepath.Join(kb, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "patterns", "backend.md"), []byte("# P\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return SyncConfig{Config: Config{KBRoot: kb, PlatformRoot: root, OutDir: out}}
}

func landCurrentManifest(t *testing.T, cfg SyncConfig) Manifest {
	t.Helper()
	_, m, err := buildCorpus(cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(filepath.Join(cfg.OutDir, "manifest.yaml"), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSync_NoOpWritesNothing(t *testing.T) {
	cfg := syncFixture(t)
	landCurrentManifest(t, cfg)
	before, _ := os.ReadFile(filepath.Join(cfg.OutDir, "manifest.yaml"))

	rep, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rep.NoOp || rep.ChunksEmbedded != 0 {
		t.Errorf("report = %+v, want NoOp with zero embeds", rep)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.OutDir, "manifest.yaml"))
	if string(before) != string(after) {
		t.Error("no-op sync rewrote the manifest — receipts would stale for nothing (AC8)")
	}
}

func TestSync_VersionGuardRefuses(t *testing.T) {
	cfg := syncFixture(t)
	m := landCurrentManifest(t, cfg)
	m.BuilderVersion = "olifant-corpus-v0.9.9"
	if err := writeManifest(filepath.Join(cfg.OutDir, "manifest.yaml"), m); err != nil {
		t.Fatal(err)
	}
	_, err := Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "drop-and-rebuild") {
		t.Fatalf("version drift must refuse to the recovery path, got %v", err)
	}
}

func TestSync_MissingManifestRefuses(t *testing.T) {
	cfg := syncFixture(t)
	_, err := Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "full rebuild") {
		t.Fatalf("missing landed manifest must point at the full rebuild, got %v", err)
	}
}

func TestSync_DryRunReportsWithoutTouching(t *testing.T) {
	cfg := syncFixture(t)
	landCurrentManifest(t, cfg)
	// Introduce drift: a new doc.
	if err := os.WriteFile(filepath.Join(cfg.KBRoot, "patterns", "frontend.md"), []byte("# F\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg.OutDir, "manifest.yaml"))

	cfg.DryRun = true
	rep, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync dry-run: %v", err)
	}
	if rep.NoOp || rep.Added != 1 {
		t.Errorf("dry-run report = %+v, want Added=1", rep)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.OutDir, "manifest.yaml"))
	if string(before) != string(after) {
		t.Error("dry-run mutated the landed manifest")
	}
}

func TestLoadManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.yaml")
	want := mkManifest(BuilderVersion, src("a.md", "s1", "universal"))
	if err := writeManifest(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifest(p)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.BuilderVersion != want.BuilderVersion || len(got.Sources) != 1 || got.Sources[0].SHA != "s1" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestStatus_ReportsDriftOffline(t *testing.T) {
	cfg := syncFixture(t)
	landCurrentManifest(t, cfg)

	rep, err := Status(cfg.Config)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Added+rep.Changed+rep.Removed != 0 || rep.VersionDrift {
		t.Errorf("clean status = %+v, want zero drift", rep)
	}

	// Drift: one new + one changed source.
	if err := os.WriteFile(filepath.Join(cfg.KBRoot, "patterns", "frontend.md"), []byte("# F\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.KBRoot, "patterns", "backend.md"), []byte("# P\n\nchanged body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err = Status(cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 1 || rep.Changed != 1 {
		t.Errorf("drift status = %+v, want Added=1 Changed=1", rep)
	}
}

// syncServers is a fake ollama+chroma pair that counts DeleteWhere calls (by
// source) and upserted ids — covering Sync's network half hermetically.
func syncServers(t *testing.T, deletedSources *[]string, upserts *int) (oURL, cURL string) {
	t.Helper()
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": embs})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(oll.Close)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/delete"):
			var req struct {
				Where map[string]interface{} `json:"where"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if s, ok := req.Where["source"].(string); ok {
				*deletedSources = append(*deletedSources, s)
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/upsert"):
			var req struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			*upserts += len(req.IDs)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/count"):
			_, _ = w.Write([]byte("42"))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "c1", "name": "corpus"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

// TestSync_FullPath_DeletesAndEmbedsOnlyTheDiff drives the complete pipeline:
// one changed source (delete + re-embed), one added (embed only), one removed
// (delete only); unchanged sources cost nothing; manifest written last.
func TestSync_FullPath_DeletesAndEmbedsOnlyTheDiff(t *testing.T) {
	cfg := syncFixture(t) // backend.md exists
	// A second doc that will be REMOVED, plus the landed manifest.
	gone := filepath.Join(cfg.KBRoot, "patterns", "gone.md")
	if err := os.WriteFile(gone, []byte("# G\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landCurrentManifest(t, cfg)

	// Drift: change backend.md, add frontend.md, remove gone.md.
	if err := os.WriteFile(filepath.Join(cfg.KBRoot, "patterns", "backend.md"), []byte("# P\n\nnew body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.KBRoot, "patterns", "frontend.md"), []byte("# F\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	upserts := 0
	cfg.OllamaURL, cfg.ChromaURL = syncServers(t, &deleted, &upserts)
	cfg.Embedder, cfg.Tenant, cfg.Database = "e", "t", "d"

	rep, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if rep.Added != 1 || rep.Changed != 1 || rep.Removed != 1 {
		t.Errorf("report = %+v, want 1/1/1", rep)
	}
	// Deletes: the changed source's old chunks + the removed source's.
	wantDeleted := map[string]bool{"patterns/backend.md": true, "patterns/gone.md": true}
	if len(deleted) != 2 || !wantDeleted[deleted[0]] || !wantDeleted[deleted[1]] {
		t.Errorf("deleted sources = %v, want backend.md + gone.md", deleted)
	}
	// Embeds: only backend.md (changed) + frontend.md (added) chunks.
	if rep.ChunksEmbedded == 0 || rep.ChunksEmbedded != upserts {
		t.Errorf("chunks embedded = %d (fake saw %d upserts)", rep.ChunksEmbedded, upserts)
	}
	// Manifest landed (written last): a re-run is now a no-op.
	rep2, err := Sync(context.Background(), cfg)
	if err != nil || !rep2.NoOp {
		t.Errorf("post-sync re-run = %+v, %v; want NoOp", rep2, err)
	}
}

func TestLiveCounts(t *testing.T) {
	var deleted []string
	upserts := 0
	_, cURL := syncServers(t, &deleted, &upserts)
	counts, err := LiveCounts(context.Background(), cURL, "t", "d")
	if err != nil {
		t.Fatalf("LiveCounts: %v", err)
	}
	if len(counts) != len(AllScopes) {
		t.Errorf("counts = %v, want one per scope", counts)
	}
}
