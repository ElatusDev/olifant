package repos

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// ===== pure helpers =====

func TestLanguageForExt(t *testing.T) {
	cases := map[string]string{
		".java": "java", ".kt": "kotlin", ".KTS": "kotlin", ".ts": "typescript",
		".tsx": "tsx", ".js": "javascript", ".jsx": "jsx", ".go": "go",
		".py": "python", ".rb": "ruby", ".swift": "swift", ".rs": "rust",
		".tf": "terraform", ".hcl": "terraform", ".sql": "sql", ".yaml": "yaml",
		".yml": "yaml", ".json": "json", ".xml": "xml", ".md": "markdown",
		".sh": "shell", ".toml": "toml", ".ini": "ini", ".properties": "properties",
		".unknownext": "text", "": "text",
	}
	for ext, want := range cases {
		if got := languageForExt(ext); got != want {
			t.Errorf("languageForExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestLooksBinary(t *testing.T) {
	if looksBinary([]byte("plain text, no nul")) {
		t.Error("text flagged as binary")
	}
	if !looksBinary([]byte{'a', 0x00, 'b'}) {
		t.Error("NUL byte not detected as binary")
	}
}

func TestSplitLinesKeepEmpty(t *testing.T) {
	if got := splitLinesKeepEmpty(nil); got != nil {
		t.Errorf("empty input = %v, want nil", got)
	}
	got := splitLinesKeepEmpty([]byte("a\n\nb\n"))
	want := []string{"a", "", "b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("split = %v, want %v", got, want)
	}
}

func TestMakeChunkID_StableAndSensitive(t *testing.T) {
	a := makeChunkID("src", "anchor", "body")
	if a != makeChunkID("src", "anchor", "body") {
		t.Error("makeChunkID not deterministic")
	}
	if a == makeChunkID("src", "anchor", "BODY") {
		t.Error("makeChunkID ignored body change")
	}
	if len(a) != 40 {
		t.Errorf("sha1 hex length = %d, want 40", len(a))
	}
}

func TestCapChars(t *testing.T) {
	if got := capChars("short", 100); got != "short" {
		t.Errorf("under cap mutated: %q", got)
	}
	if got := capChars("abcdef", 3); got != "abc" {
		t.Errorf("ascii cap = %q, want abc", got)
	}
	// "aé" = bytes [0x61 0xC3 0xA9]; cap at 2 must back off the partial rune.
	if got := capChars("aé", 2); got != "a" {
		t.Errorf("multibyte cap = %q, want a (no partial rune)", got)
	}
}

func TestDefaultRepos(t *testing.T) {
	specs := DefaultRepos("/plat")
	if len(specs) != 7 {
		t.Fatalf("DefaultRepos len = %d, want 7", len(specs))
	}
	if specs[0].Name != "infra" || specs[0].Scope != "infra" {
		t.Errorf("first spec = %+v, want infra (smallest-first)", specs[0])
	}
	if specs[len(specs)-1].Name != "core-api" {
		t.Errorf("last spec = %q, want core-api", specs[len(specs)-1].Name)
	}
	if specs[0].Path != filepath.Join("/plat", "infra") {
		t.Errorf("path = %q", specs[0].Path)
	}
}

func TestChunkMetadataForChroma(t *testing.T) {
	c := corpus.Chunk{
		Source: "core-api/Foo.java", Scope: "backend", DocType: "code",
		SourceSHA: "deadbeef", SourceAnchor: "core-api/Foo.java#L1-L80",
		Title: "Foo.java:L1-L80",
		Metadata: corpus.ChunkMetadata{
			Section:       "L1-L80",
			CitesOutbound: []string{"D154", "AP78"},
			TechTags:      []string{"java", "core-api"},
		},
	}
	m := chunkMetadataForChroma(c)
	if m["source"] != "core-api/Foo.java" || m["scope"] != "backend" {
		t.Errorf("base fields = %v", m)
	}
	if m["cites_outbound"] != "D154,AP78" {
		t.Errorf("cites_outbound = %v, want joined", m["cites_outbound"])
	}
	if m["tech_tags"] != "java,core-api" {
		t.Errorf("tech_tags = %v", m["tech_tags"])
	}

	// Optional fields omitted when empty.
	bare := chunkMetadataForChroma(corpus.Chunk{Source: "x", Scope: "y", DocType: "code"})
	if _, ok := bare["title"]; ok {
		t.Error("empty title should be omitted")
	}
	if _, ok := bare["tech_tags"]; ok {
		t.Error("empty tech_tags should be omitted")
	}
}

// ===== Chunk =====

func TestChunk_EmptyFileIsNil(t *testing.T) {
	if got := Chunk(SourceFile{Bytes: nil}, "backend"); got != nil {
		t.Errorf("empty file = %v, want nil", got)
	}
}

func TestChunk_SingleWindowFile(t *testing.T) {
	f := SourceFile{
		RepoName: "core-api", RelPath: "src/Foo.java", SHA: "abc123",
		Language: "java", Bytes: []byte("line1\nline2\nline3"),
	}
	chunks := Chunk(f, "backend")
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	c := chunks[0]
	if c.Scope != "backend" || c.DocType != "code" {
		t.Errorf("scope/doctype = %s/%s", c.Scope, c.DocType)
	}
	if c.SourceAnchor != "core-api/src/Foo.java#L1-L3" {
		t.Errorf("anchor = %q", c.SourceAnchor)
	}
	if !strings.HasPrefix(c.Body, "[code/java] core-api/src/Foo.java#L1-L3\n") {
		t.Errorf("breadcrumb header missing:\n%s", c.Body)
	}
	if c.SourceSHA != "abc123" {
		t.Errorf("sha = %q", c.SourceSHA)
	}
}

func TestChunk_MultiWindowOverlap(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString("line\n")
	}
	f := SourceFile{RepoName: "r", RelPath: "a.go", Language: "go", Bytes: []byte(sb.String())}
	chunks := Chunk(f, "backend")
	if len(chunks) < 2 {
		t.Fatalf("200-line file should produce multiple chunks, got %d", len(chunks))
	}
	// Window 80, step 60 → second chunk starts at line 61.
	if !strings.Contains(chunks[1].SourceAnchor, "#L61-") {
		t.Errorf("second chunk anchor = %q, want overlap start at L61", chunks[1].SourceAnchor)
	}
	// Last chunk ends at line 200.
	last := chunks[len(chunks)-1]
	if !strings.HasSuffix(last.SourceAnchor, "-L200") {
		t.Errorf("last anchor = %q, want end L200", last.SourceAnchor)
	}
}

// ===== Walk (real temp git repo) =====

func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	return dir
}

func TestWalk(t *testing.T) {
	dir := gitInitRepo(t)
	write := func(rel string, b []byte) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/Foo.java", []byte("class Foo {}\n"))
	write("bin.dat", []byte{0x00, 0x01, 0x02})     // binary — NUL → skipped
	write("big.txt", make([]byte, MaxFileBytes+1)) // oversized → skipped

	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	files, err := Walk(dir, "core-api")
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.RelPath)
		if f.RepoName != "core-api" {
			t.Errorf("repo name = %q", f.RepoName)
		}
	}
	if len(got) != 1 || got[0] != "src/Foo.java" {
		t.Errorf("walked files = %v, want only src/Foo.java (binary+oversized skipped)", got)
	}
	if files[0].Language != "java" || files[0].SHA == "" {
		t.Errorf("file meta = lang:%q sha:%q", files[0].Language, files[0].SHA)
	}
}

func TestGitLsFilesSHAs_NonRepoIsEmpty(t *testing.T) {
	m, err := gitLsFilesSHAs(t.TempDir())
	if err != nil {
		t.Fatalf("non-repo dir errored: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("non-repo dir returned %d files, want 0", len(m))
	}
}

// ===== writeChunksNDJSON =====

func TestWriteChunksNDJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "backend.ndjson")
	chunks := []corpus.Chunk{
		{ChunkID: "id1", Source: "r/a.go", Scope: "backend", Body: "a <b> c"},
		{ChunkID: "id2", Source: "r/b.go", Scope: "backend", Body: "x"},
	}
	if err := writeChunksNDJSON(p, chunks); err != nil {
		t.Fatalf("writeChunksNDJSON: %v", err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
		var c corpus.Chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("line %d not valid json: %v", n, err)
		}
		if n == 1 && !strings.Contains(sc.Text(), "<b>") {
			t.Errorf("HTML was escaped (SetEscapeHTML(false) not honored): %s", sc.Text())
		}
	}
	if n != 2 {
		t.Errorf("wrote %d lines, want 2", n)
	}
}

// ===== Ollama + Chroma test doubles =====

// fakeOllama serves /api/version and /api/embed, returning one fixed-width
// vector per input. embedFails, when set, fails batched (>1 input) requests so
// the per-chunk fallback path is exercised.
func fakeOllama(t *testing.T, embedFails bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.1.0"}`))
		case "/api/embed":
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if embedFails && len(req.Input) > 1 {
				http.Error(w, "overloaded", http.StatusInternalServerError)
				return
			}
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2, 0.3}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Model: req.Model, Embeddings: embs})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeChroma serves the minimal surface Ingest touches.
func fakeChroma(t *testing.T, upserts *int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_, _ = w.Write([]byte(`{"nanosecond heartbeat": 1}`))
		case strings.HasSuffix(r.URL.Path, "/tenants"), strings.HasSuffix(r.URL.Path, "/databases"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"coll-1","name":"code"}`))
		case strings.HasSuffix(r.URL.Path, "/upsert"):
			if upserts != nil {
				var req chroma.UpsertRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				*upserts += len(req.IDs)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// ===== indexBatched =====

func sampleChunks(n int) []corpus.Chunk {
	cs := make([]corpus.Chunk, n)
	for i := range cs {
		cs[i] = corpus.Chunk{
			ChunkID: strings.Repeat("0", 11) + string(rune('a'+i)),
			Source:  "r/a.go", Scope: "backend", DocType: "code",
			Body: "package main // chunk body",
		}
	}
	return cs
}

func TestIndexBatched_HappyPath(t *testing.T) {
	oc := ollama.New(fakeOllama(t, false))
	upserts := 0
	cc := chroma.New(fakeChroma(t, &upserts), "", "")

	ups, batches, err := indexBatched(context.Background(), oc, cc, "coll-1", "bge-m3", sampleChunks(5), 2, true)
	if err != nil {
		t.Fatalf("indexBatched: %v", err)
	}
	if ups != 5 {
		t.Errorf("upserted = %d, want 5", ups)
	}
	if batches != 3 { // 2+2+1
		t.Errorf("batches = %d, want 3", batches)
	}
	if upserts != 5 {
		t.Errorf("chroma saw %d ids, want 5", upserts)
	}
}

func TestIndexBatched_PerChunkFallback(t *testing.T) {
	// Batched embed fails → falls back to per-chunk single embeds (which succeed).
	oc := ollama.New(fakeOllama(t, true))
	upserts := 0
	cc := chroma.New(fakeChroma(t, &upserts), "", "")

	ups, _, err := indexBatched(context.Background(), oc, cc, "coll-1", "bge-m3", sampleChunks(3), 3, false)
	if err != nil {
		t.Fatalf("indexBatched fallback: %v", err)
	}
	if ups != 3 {
		t.Errorf("fallback upserted = %d, want 3", ups)
	}
}

// ===== Ingest =====

func TestIngest_DryRun(t *testing.T) {
	repo := gitInitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = repo
	_ = add.Run()

	stats, err := Ingest(context.Background(), IngestConfig{
		Repos:  []RepoSpec{{Name: "core-api", Path: repo, Scope: "backend"}},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Ingest dry-run: %v", err)
	}
	if stats.FilesRead != 1 || stats.ChunksProduced < 1 {
		t.Errorf("dry-run stats = %+v", stats)
	}
	if stats.ChunksUpserted != 0 {
		t.Errorf("dry-run must not upsert, got %d", stats.ChunksUpserted)
	}
}

func TestIngest_FullHappyPath(t *testing.T) {
	repo := gitInitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main\nfunc main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = repo
	_ = add.Run()

	upserts := 0
	outDir := filepath.Join(t.TempDir(), "code")
	stats, err := Ingest(context.Background(), IngestConfig{
		Repos:       []RepoSpec{{Name: "core-api", Path: repo, Scope: "backend"}},
		OutDir:      outDir,
		WriteNDJSON: true,
		OllamaURL:   fakeOllama(t, false),
		ChromaURL:   fakeChroma(t, &upserts),
		Embedder:    "bge-m3",
		Verbose:     true,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.ReposProcessed != 1 || stats.ChunksUpserted < 1 {
		t.Errorf("stats = %+v", stats)
	}
	if upserts != stats.ChunksUpserted {
		t.Errorf("chroma saw %d, stats report %d", upserts, stats.ChunksUpserted)
	}
	// NDJSON was written for the backend scope.
	if _, err := os.Stat(filepath.Join(outDir, "backend.ndjson")); err != nil {
		t.Errorf("backend.ndjson not written: %v", err)
	}
}

func TestIngest_OllamaUnreachable(t *testing.T) {
	// A closed server URL makes the pre-flight Version() probe fail.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	_, err := Ingest(context.Background(), IngestConfig{
		Repos:     []RepoSpec{},
		OllamaURL: dead.URL,
		ChromaURL: dead.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "ollama unreachable") {
		t.Errorf("want ollama-unreachable error, got %v", err)
	}
}

func TestIngest_SkipsMissingRepoPath(t *testing.T) {
	upserts := 0
	stats, err := Ingest(context.Background(), IngestConfig{
		Repos:     []RepoSpec{{Name: "ghost", Path: "/no/such/path", Scope: "backend"}},
		OllamaURL: fakeOllama(t, false),
		ChromaURL: fakeChroma(t, &upserts),
		Embedder:  "bge-m3",
		Verbose:   true,
	})
	if err != nil {
		t.Fatalf("Ingest with missing repo path should not error: %v", err)
	}
	if stats.FilesRead != 0 {
		t.Errorf("missing repo should read 0 files, got %d", stats.FilesRead)
	}
}
