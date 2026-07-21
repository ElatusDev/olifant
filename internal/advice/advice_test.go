package advice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/prompt"
)

func TestBucket(t *testing.T) {
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
		if got := Bucket(c.c); got != c.want {
			t.Errorf("%s: Bucket = %q, want %q", c.name, got, c.want)
		}
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
		got := ExtractCodeSignals(c.code)
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

// adviceStack stands up a fake Ollama (embed only — errors on /api/generate) +
// a Chroma returning one pattern chunk, recording queried collections.
func adviceStack(t *testing.T) (ollamaURL, chromaURL string) {
	t.Helper()
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embed":
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
		case "/api/generate":
			t.Error("advice.Run called the synthesizer (/api/generate) — must stay retrieval-only (D-PP2)")
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(oll.Close)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections"):
			var b struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			_, _ = w.Write([]byte(`{"id":"c1","name":"` + b.Name + `"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["Domain Object pattern"]],"metadatas":[[{"source":"patterns/backend.md","scope":"backend","doc_type":"pattern"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

func TestRun_BucketsAndCites_NoSynth(t *testing.T) {
	oURL, cURL := adviceStack(t)
	res, err := Run(context.Background(), Config{
		CodeBody:  "class Foo { void m(){ when(repo.save(any())); } }",
		Scopes:    []string{"backend", "universal"},
		OllamaURL: oURL, ChromaURL: cURL, Embedder: "m",
		TopN: 8,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The fake returns a pattern chunk → prefer bucket populated.
	if len(res.Prefer) == 0 {
		t.Fatalf("expected a prefer chunk, got avoid=%d prefer=%d conv=%d", len(res.Avoid), len(res.Prefer), len(res.Conventions))
	}
	if res.Prefer[0].Source != "patterns/backend.md" {
		t.Errorf("prefer source = %q", res.Prefer[0].Source)
	}
	// Cites accessor returns per-bucket cite union (none here, but must not panic).
	if got := res.Cites("prefer"); len(got) != 0 {
		t.Errorf("cites(prefer) = %v, want empty", got)
	}
}
