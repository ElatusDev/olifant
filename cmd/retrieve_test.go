package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/prompt"
)

func TestInferScopes(t *testing.T) {
	root := "/plat"
	cases := []struct {
		cwd  string
		want []string
	}{
		{"/plat/platform-core-api/security", []string{"backend", "universal"}},
		{"/plat/AkademiaPlusWebApp", []string{"webapp", "universal"}},
		{"/plat/akademia-plus-go/src", []string{"mobile", "universal"}},
		{"/plat/elatusdev-infra", []string{"infra", "universal"}},
		{"/plat/core-api-e2e", []string{"e2e", "universal"}},
		{"/plat", nil},        // platform root itself
		{"/elsewhere/x", nil}, // outside the tree
		{"/plat/unknown-repo", nil},
	}
	for _, c := range cases {
		got := inferScopes(c.cwd, root)
		if len(got) != len(c.want) {
			t.Errorf("inferScopes(%q) = %v, want %v", c.cwd, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("inferScopes(%q) = %v, want %v", c.cwd, got, c.want)
				break
			}
		}
	}
}

func TestRetrieveEconomy_SumsLocalKBSourcesOnly(t *testing.T) {
	kb := t.TempDir()
	if err := os.MkdirAll(filepath.Join(kb, "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "decisions", "log.md"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	got := retrieveEconomy(kb, []string{
		"decisions/log.md",             // counted: 1000
		"core-api@abc123:src/Foo.java", // repo chunk — skipped
		"missing/file.md",              // absent — skipped
	})
	if got != 1000 {
		t.Errorf("economy = %d, want 1000", got)
	}
}

func TestAdviceBucket(t *testing.T) {
	cases := []struct {
		name string
		c    prompt.ContextChunk
		want string
	}{
		{"failure_modes family", prompt.ContextChunk{Scope: "backend/failure_modes"}, "avoid"},
		{"anti_pattern doc_type", prompt.ContextChunk{Scope: "backend/corpus", DocType: "anti_pattern"}, "avoid"},
		{"AP cite fallback", prompt.ContextChunk{Scope: "backend/corpus", DocType: "doc", Cites: []string{"AP184"}}, "avoid"},
		{"pattern doc_type", prompt.ContextChunk{Scope: "backend/corpus", DocType: "pattern"}, "prefer"},
		{"standard", prompt.ContextChunk{Scope: "backend/corpus", DocType: "standard"}, "convention"},
		{"decision", prompt.ContextChunk{Scope: "universal/corpus", DocType: "decision", Cites: []string{"D259"}}, "convention"},
		{"plain doc", prompt.ContextChunk{Scope: "backend/corpus", DocType: "doc"}, "convention"},
	}
	for _, c := range cases {
		if got := adviceBucket(c.c); got != c.want {
			t.Errorf("%s: adviceBucket = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGroupAdvice_BucketsAndRenders(t *testing.T) {
	res := &prompt.ContextResult{Chunks: []prompt.ContextChunk{
		{Source: "eval/failure-modes/v1.yaml#fm3", Scope: "backend/failure_modes", Body: "use streams not manual loops", Cites: []string{"AP20"}},
		{Source: "patterns/backend.md", Scope: "backend/corpus", DocType: "pattern", Body: "Domain Object pattern"},
		{Source: "standards/CODE-QUALITY.yaml", Scope: "universal/corpus", DocType: "standard", Body: "one sentence per public API"},
	}}
	g := groupAdvice("code advice: Foo.java", []string{"backend", "universal"}, res)
	if len(g.Avoid) != 1 || len(g.Prefer) != 1 || len(g.Conventions) != 1 {
		t.Fatalf("buckets avoid=%d prefer=%d conv=%d, want 1/1/1", len(g.Avoid), len(g.Prefer), len(g.Conventions))
	}
	md := renderAdviceMD("code advice: Foo.java", res)
	for _, want := range []string{"⛔ Avoid", "✅ Prefer", "📐 Conventions", "Domain Object", "AP20"} {
		if !strings.Contains(md, want) {
			t.Errorf("renderAdviceMD missing %q\n%s", want, md)
		}
	}
	// Empty result → explicit no-op line, never a crash.
	if empty := renderAdviceMD("q", &prompt.ContextResult{}); !strings.Contains(empty, "No applicable rules") {
		t.Errorf("empty render = %q", empty)
	}
}

func TestFilterAdviceChunks(t *testing.T) {
	chunks := []prompt.ContextChunk{
		{Source: "standards/CODE-QUALITY-STANDARD.md", Scope: "universal/corpus", DocType: "standard"},   // keep
		{Source: "workflows/core-api/x-workflow.md", Scope: "backend/corpus", DocType: "workflow"},       // drop (process)
		{Source: "eval/failure-modes/v1.yaml#fm1", Scope: "backend/failure_modes"},                       // keep (family)
		{Source: "claude-memory/projects/x/memory/MEMORY.md", Scope: "universal/corpus", DocType: "doc"}, // drop (noise source)
		{Source: "for-you/README.md", Scope: "universal/corpus", DocType: "doc"},                         // drop (noise source)
		{Source: "tech-encyclopedia/backend/java.md", Scope: "backend/corpus", DocType: "doc"},           // keep (guide)
		{Source: "anti-patterns/catalog.yaml#AP4", Scope: "universal/corpus", DocType: "anti_pattern"},   // keep
	}
	got := filterAdviceChunks(chunks, 10)
	want := map[string]bool{
		"standards/CODE-QUALITY-STANDARD.md": true,
		"eval/failure-modes/v1.yaml#fm1":     true,
		"tech-encyclopedia/backend/java.md":  true,
		"anti-patterns/catalog.yaml#AP4":     true,
	}
	if len(got) != len(want) {
		t.Fatalf("kept %d chunks, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if !want[c.Source] {
			t.Errorf("kept unexpected source %q", c.Source)
		}
	}
	// keep-limit truncates.
	if trunc := filterAdviceChunks(chunks, 2); len(trunc) != 2 {
		t.Errorf("truncate to 2: got %d", len(trunc))
	}
}

func TestExtractCodeSignals(t *testing.T) {
	cases := []struct {
		name string
		code string
		want []string // substrings that must appear; empty = expect no signals
	}{
		{"any matcher", "when(repo.save(any())).thenReturn(x);", []string{"any() matcher"}},
		{"for loop", "for (int i = 0; i < n; i++) {}", []string{"manual loop"}},
		{"raw sql", `@Query("SELECT * FROM t")`, []string{"raw SQL"}},
		{"field injection", "@Autowired private Repo repo;", []string{"field injection"}},
		{"multiple", "for (X x : xs) { when(m.f(any())); }", []string{"any() matcher", "manual loop"}},
		{"ts console", "console.log('x'); const y: any = z;", []string{"console logging", "loose typing"}},
		{"react hooks", "useEffect(() => setX(1), []);", []string{"React hooks"}},
		{"go print/panic", "fmt.Println(x); if err != nil { panic(err) }", []string{"fmt.Print", "error handling"}},
		{"python print", "print(value)", []string{"print()"}},
		{"clean go", "return xs, nil", nil},
	}
	for _, c := range cases {
		got := extractCodeSignals(c.code)
		if len(c.want) == 0 {
			if got != "" {
				t.Errorf("%s: expected no signals, got %q", c.name, got)
			}
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: signals %q missing %q", c.name, got, w)
			}
		}
	}
}

func TestRetrieveFile_EmptyDegradesToExitZero(t *testing.T) {
	dir := t.TempDir()
	kbAndCwd(t, dir)
	empty := filepath.Join(dir, "Scratch.java")
	if err := os.WriteFile(empty, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Retrieve([]string{"-no-record", "-file", empty}); code != 0 {
		t.Errorf("empty file: exit %d, want 0 (degrade, never error)", code)
	}
}

// AC3/AC5: --file runs on the fast retrieval lane (NO /api/generate) and queries
// the failure_modes family (D-PP3), writing nothing.
func TestRetrieveFile_NoSynthAndQueriesFailureModes(t *testing.T) {
	var mu sync.Mutex
	ensured := map[string]bool{}
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.5.0"}`))
		case "/api/embed":
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
		case "/api/generate":
			t.Error("retrieve --file called the synthesizer (/api/generate) — must stay retrieval-only (D-PP2)")
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(oll.Close)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_, _ = w.Write([]byte(`{"nanosecond heartbeat":1}`))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			var b struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			ensured[b.Name] = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"c1","name":"` + b.Name + `"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc"]],"metadatas":[[{"source":"patterns/backend.md","scope":"backend","doc_type":"pattern"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	t.Setenv("OLIFANT_OLLAMA_URL", oll.URL)
	t.Setenv("OLIFANT_CHROMA_URL", chr.URL)
	t.Setenv("OLIFANT_EMBEDDER", "bge-m3")

	dir := t.TempDir()
	kbAndCwd(t, dir)
	code := filepath.Join(dir, "Foo.java")
	if err := os.WriteFile(code, []byte("class Foo { void m(){ for(int i=0;i<n;i++){} } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := Retrieve([]string{"-no-record", "-scope", "backend", "-file", code}); rc != 0 {
		t.Fatalf("retrieve --file: exit %d, want 0", rc)
	}
	mu.Lock()
	gotFM, leakedCode := ensured["failure_modes_backend"], ensured["code_backend"]
	mu.Unlock()
	if !gotFM {
		t.Error("retrieve --file did not query failure_modes_backend (D-PP3)")
	}
	if leakedCode {
		t.Error("retrieve --file queried code_backend — advice must use rule families only (P3)")
	}
}

// kbAndCwd creates a minimal knowledge-base marker under dir and chdirs there.
func kbAndCwd(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "knowledge-base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "knowledge-base", "README.md"), []byte("kb"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func TestRetrieve_MissingQuestionAndStackDown(t *testing.T) {
	if code := Retrieve([]string{"-no-record"}); code != 2 {
		t.Errorf("missing question: exit %d, want 2", code)
	}
	// Fake stack answers embed; a real question against fakeStack must succeed
	// without any /api/generate call (retrieval-only path).
	fakeStack(t, "unused")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "knowledge-base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "knowledge-base", "README.md"), []byte("kb"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if code := Retrieve([]string{"-no-record", "-scope", "backend", "how is tenant scoping enforced"}); code != 0 {
		t.Errorf("fake-stack retrieve: exit %d, want 0", code)
	}
	if code := Retrieve([]string{"-no-record", "-format", "md", "-scope", "backend", "same question md"}); code != 0 {
		t.Errorf("md format: exit %d, want 0", code)
	}
}
