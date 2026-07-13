package repos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/corpus"
)

// fixtureRepo creates a temp git repo with the given files staged.
func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := gitInitRepo(t)
	stageFiles(t, dir, files)
	return dir
}

func stageFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
}

// syncFixture: two repos sharing ONE scope (the code_mobile trap) plus a
// backend repo — manifest landed from the initial state.
func syncFixture(t *testing.T) IngestConfig {
	t.Helper()
	goApp := fixtureRepo(t, map[string]string{"App.tsx": "export const A = 1\n"})
	central := fixtureRepo(t, map[string]string{"App.tsx": "export const B = 2\n"})
	api := fixtureRepo(t, map[string]string{"src/Foo.java": "class Foo {}\n"})

	cfg := IngestConfig{
		Repos: []RepoSpec{
			{Name: "akademia-plus-go", Path: goApp, Scope: "mobile"},
			{Name: "akademia-plus-central", Path: central, Scope: "mobile"},
			{Name: "core-api", Path: api, Scope: "backend"},
		},
		ManifestPath: filepath.Join(t.TempDir(), "repo-manifest.yaml"),
	}
	return cfg
}

func landManifest(t *testing.T, cfg IngestConfig) {
	t.Helper()
	scoped, _, err := CollectChunks(cfg)
	if err != nil {
		t.Fatalf("CollectChunks: %v", err)
	}
	if err := corpus.WriteManifest(cfg.ManifestPath, BuildManifest(scoped)); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func TestBuildManifest_RepoQualifiedSources(t *testing.T) {
	cfg := syncFixture(t)
	scoped, _, err := CollectChunks(cfg)
	if err != nil {
		t.Fatalf("CollectChunks: %v", err)
	}
	m := BuildManifest(scoped)

	if m.BuilderVersion != SyncBuilderVersion {
		t.Errorf("builder version = %q", m.BuilderVersion)
	}
	// The two mobile repos' same-named files must be DISTINCT sources.
	var paths []string
	for _, s := range m.Sources {
		paths = append(paths, s.Path)
		if s.DocType != "code" || s.SHA == "" || s.Chunks == 0 {
			t.Errorf("source %+v missing doc_type/sha/chunks", s)
		}
	}
	joined := strings.Join(paths, "|")
	for _, want := range []string{"akademia-plus-go/App.tsx", "akademia-plus-central/App.tsx", "core-api/src/Foo.java"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sources %v missing %s", paths, want)
		}
	}
	if len(m.Sources) != 3 {
		t.Errorf("sources = %d, want 3", len(m.Sources))
	}
	if m.ByScope["mobile"] == 0 || m.ByScope["backend"] == 0 {
		t.Errorf("by_scope = %v", m.ByScope)
	}
}

func TestRepoSync_MissingManifestRefuses(t *testing.T) {
	cfg := syncFixture(t)
	_, err := Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "repo ingest") {
		t.Errorf("missing manifest err = %v, want pointer to full ingest", err)
	}
}

func TestRepoSync_VersionGuardRefuses(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)
	// Corrupt the landed builder version.
	m, err := corpus.LoadManifest(cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m.BuilderVersion = "olifant-repo-v0.9.9"
	if err := corpus.WriteManifest(cfg.ManifestPath, m); err != nil {
		t.Fatal(err)
	}
	_, err = Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "builder version drift") {
		t.Errorf("version drift err = %v", err)
	}
}

func TestRepoSync_NoOpWritesNothing(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)
	before, err := os.Stat(cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := Sync(context.Background(), cfg) // no fake stack: a no-op must not dial anything
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rep.NoOp {
		t.Errorf("report = %+v, want NoOp", rep)
	}
	after, err := os.Stat(cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("no-op rewrote the manifest")
	}
}

func TestRepoSync_DryRunReportsWithoutTouching(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)
	stageFiles(t, cfg.Repos[2].Path, map[string]string{"src/Bar.java": "class Bar {}\n"})

	cfg.DryRun = true
	rep, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync dry-run: %v", err)
	}
	if rep.Added != 1 || rep.ChunksEmbedded != 0 {
		t.Errorf("dry-run report = %+v, want Added=1, nothing embedded", rep)
	}
	// Manifest untouched → a real sync still sees the drift.
	cfg.DryRun = false
	if rep2, _ := Status(cfg); rep2.Added != 1 {
		t.Errorf("post-dry-run status = %+v, want Added=1", rep2)
	}
}

// repoSyncServers mirrors corpus sync_test's fake ollama+chroma pair, also
// capturing the collection names ensured (scope-targeting proof).
func repoSyncServers(t *testing.T, deletedSources *[]string, upserts *int, collections *[]string) (oURL, cURL string) {
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
		case strings.HasSuffix(r.URL.Path, "/collections"):
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Name != "" {
				*collections = append(*collections, req.Name)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "c-" + req.Name, "name": req.Name})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

func TestRepoSync_FullPath_DeletesAndEmbedsOnlyTheDiff(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)

	// Drift: change go-app's App.tsx, add a backend file, remove central's App.tsx.
	stageFiles(t, cfg.Repos[0].Path, map[string]string{"App.tsx": "export const A = 999\n"})
	stageFiles(t, cfg.Repos[2].Path, map[string]string{"src/Bar.java": "class Bar {}\n"})
	central := cfg.Repos[1].Path
	if err := os.Remove(filepath.Join(central, "App.tsx")); err != nil {
		t.Fatal(err)
	}
	rm := exec.Command("git", "add", "-A")
	rm.Dir = central
	if out, err := rm.CombinedOutput(); err != nil {
		t.Fatalf("git add -A (removal): %v\n%s", err, out)
	}

	var deleted, colls []string
	upserts := 0
	cfg.OllamaURL, cfg.ChromaURL = repoSyncServers(t, &deleted, &upserts, &colls)
	cfg.Embedder, cfg.Tenant, cfg.Database = "e", "t", "d"

	rep, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if rep.Added != 1 || rep.Changed != 1 || rep.Removed != 1 {
		t.Errorf("report = %+v, want 1/1/1", rep)
	}
	// Deletes are repo-qualified — central's removal must NOT name go's file.
	wantDeleted := map[string]bool{
		"akademia-plus-go/App.tsx":      true, // changed → old chunks deleted
		"akademia-plus-central/App.tsx": true, // removed
	}
	if len(deleted) != 2 || !wantDeleted[deleted[0]] || !wantDeleted[deleted[1]] {
		t.Errorf("deleted sources = %v", deleted)
	}
	// Both deletes target the mobile collection; the add embeds into backend.
	joined := strings.Join(colls, "|")
	if !strings.Contains(joined, "code_mobile") || !strings.Contains(joined, "code_backend") {
		t.Errorf("collections ensured = %v", colls)
	}
	if rep.ChunksEmbedded == 0 || rep.ChunksEmbedded != upserts {
		t.Errorf("embedded=%d fake saw %d", rep.ChunksEmbedded, upserts)
	}
	// Manifest written last → immediate re-run is a no-op.
	rep2, err := Sync(context.Background(), cfg)
	if err != nil || !rep2.NoOp {
		t.Errorf("re-run = %+v, %v; want NoOp", rep2, err)
	}
}

func TestRepoStatus_ReportsDriftOffline(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)

	rep, err := Status(cfg)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Added+rep.Changed+rep.Removed != 0 || rep.VersionDrift {
		t.Errorf("clean status = %+v", rep)
	}

	stageFiles(t, cfg.Repos[2].Path, map[string]string{"src/Baz.java": "class Baz {}\n"})
	rep, err = Status(cfg)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Added != 1 {
		t.Errorf("drift status = %+v, want Added=1", rep)
	}
}

func TestIngest_MintsManifestLast(t *testing.T) {
	cfg := syncFixture(t)
	upserts := 0
	cfg.OllamaURL = fakeOllama(t, false)
	cfg.ChromaURL = fakeChroma(t, &upserts)
	cfg.Embedder, cfg.Tenant, cfg.Database = "e", "t", "d"

	if _, err := Ingest(context.Background(), cfg); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	m, err := corpus.LoadManifest(cfg.ManifestPath)
	if err != nil {
		t.Fatalf("manifest not minted: %v", err)
	}
	if m.BuilderVersion != SyncBuilderVersion || len(m.Sources) != 3 {
		t.Errorf("minted manifest = builder %q, %d sources", m.BuilderVersion, len(m.Sources))
	}
	// Bootstrap complete: sync is now a no-op.
	rep, err := Sync(context.Background(), cfg)
	if err != nil || !rep.NoOp {
		t.Errorf("post-ingest sync = %+v, %v; want NoOp", rep, err)
	}
}

func TestRepoSync_MissingRepoDirRefuses(t *testing.T) {
	cfg := syncFixture(t)
	landManifest(t, cfg)
	cfg.Repos[1].Path = filepath.Join(t.TempDir(), "gone") // simulate unmounted checkout

	_, err := Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("missing-dir err = %v, want refusal (mass-delete guard)", err)
	}
}

func TestIngest_RefusesPartialManifestMint(t *testing.T) {
	cfg := syncFixture(t)
	cfg.Repos[0].Path = filepath.Join(t.TempDir(), "gone")
	cfg.OllamaURL = fakeOllama(t, false)
	cfg.ChromaURL = fakeChroma(t, nil)
	cfg.Embedder, cfg.Tenant, cfg.Database = "e", "t", "d"

	_, err := Ingest(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "partial manifest") {
		t.Errorf("partial-mint err = %v, want refusal", err)
	}
}

func TestRepoSyncAndStatus_RequireManifestPath(t *testing.T) {
	if _, err := Sync(context.Background(), IngestConfig{}); err == nil {
		t.Error("Sync without ManifestPath should error")
	}
	if _, err := Status(IngestConfig{}); err == nil {
		t.Error("Status without ManifestPath should error")
	}
}

func TestCodeCollectionName(t *testing.T) {
	if got := codeCollectionName("platform-process"); got != "code_platform_process" {
		t.Errorf("codeCollectionName = %q", got)
	}
}
