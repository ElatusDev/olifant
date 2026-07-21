package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/advice"
	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/prompt"
)

// adviceFakeStack: embed + Chroma returning an anti_pattern chunk citing AP4;
// /api/generate is a test failure (advice must be retrieval-only).
func adviceFakeStack(t *testing.T) (ollamaURL, chromaURL string) {
	t.Helper()
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
			t.Error("advice case called the synthesizer — must stay retrieval-only (D269)")
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
			_, _ = w.Write([]byte(`{"id":"c1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["AP4: any() Matchers in Mockito"]],"metadatas":[[{"source":"anti-patterns/catalog.md","scope":"universal","doc_type":"anti_pattern"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

// TestRun_AdviceCase drives an advice-surface case through the full runner:
// dispatch → advice.Run → score → clean tally, with advice.yaml persisted.
func TestRun_AdviceCase(t *testing.T) {
	oURL, cURL := adviceFakeStack(t)
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	t.Setenv("OLIFANT_OLLAMA_URL", oURL)
	t.Setenv("OLIFANT_CHROMA_URL", cURL)
	t.Setenv("OLIFANT_EMBEDDER", "bge-m3")

	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(kb, 0o755); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	suite := &Suite{SuiteID: "advice-quality-v1", Cases: []Case{
		{ID: "adv-any", Scope: []string{"backend"},
			FileContent: "when(repo.save(any()))",
			Advice:      &AdviceExpected{ExpectAvoid: []string{"AP4"}}},
	}}
	report, err := Run(context.Background(), RunConfig{Suite: suite, PlatformRoot: root, KBRoot: kb, OutDir: out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Cases) != 1 || report.Cases[0].AdviceScore == nil {
		t.Fatalf("expected 1 advice case result with a score, got %+v", report.Cases)
	}
	if !report.Cases[0].AdviceScore.Passed {
		t.Errorf("advice case should pass (AP4 in avoid): %+v", report.Cases[0].AdviceScore)
	}
	if report.CleanCases != 1 {
		t.Errorf("CleanCases = %d, want 1", report.CleanCases)
	}
	if report.TotalBlockers != 0 {
		t.Errorf("advice cases must never contribute BLOCKERs (D269): %d", report.TotalBlockers)
	}
	if _, err := os.Stat(filepath.Join(out, report.RunID, "adv-any", "advice.yaml")); err != nil {
		t.Errorf("advice.yaml not persisted: %v", err)
	}
}

// TestRunAdviceCase drives the full advice case path against a fake stack: a
// Chroma returning an anti-pattern chunk citing AP4 → the avoid expectation
// scores a hit, the case passes, and no synthesizer is called.
func TestRunAdviceCase(t *testing.T) {
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
			t.Error("advice case called the synthesizer — must stay retrieval-only (D269)")
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
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["AP4: any() Matchers in Mockito"]],"metadatas":[[{"source":"anti-patterns/catalog.md","scope":"universal","doc_type":"anti_pattern"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)

	rt := config.Runtime{OllamaURL: oll.URL, ChromaURL: chr.URL, Embedder: "m", ChromaTenant: "t", ChromaDatabase: "d"}
	c := Case{
		ID: "adv-1", Scope: []string{"backend"},
		FileContent: "when(repo.save(any()))",
		Advice:      &AdviceExpected{ExpectAvoid: []string{"AP4"}},
	}
	res := runAdviceCase(context.Background(), c, t.TempDir(), time.Now(), rt, 8, 30)
	if res.Error != "" {
		t.Fatalf("runAdviceCase error: %s", res.Error)
	}
	if res.AdviceScore == nil || !res.AdviceScore.Passed {
		t.Fatalf("expected pass, got %+v", res.AdviceScore)
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieved chunks")
	}
}

func TestScoreAdvice(t *testing.T) {
	// A result whose avoid bucket cites AP4, prefer cites nothing useful.
	r := &advice.Result{
		Avoid: []prompt.ContextChunk{
			{Source: "anti-patterns/catalog.md", Scope: "universal/corpus", DocType: "anti_pattern", Cites: []string{"AP4", "ABT-01"}},
		},
		Prefer: []prompt.ContextChunk{
			{Source: "patterns/backend.md", Scope: "backend/corpus", DocType: "pattern", Cites: []string{"D17"}},
		},
	}

	// All expected cites surface in their buckets → pass.
	pass := scoreAdvice(&AdviceExpected{ExpectAvoid: []string{"AP4"}, ExpectPrefer: []string{"D17"}}, r)
	if !pass.Passed {
		t.Fatalf("expected pass, got %+v", pass)
	}
	if len(pass.Avoid.Hit) != 1 || pass.Avoid.Hit[0] != "AP4" {
		t.Errorf("avoid hit = %v", pass.Avoid.Hit)
	}

	// A cite expected in avoid but only present elsewhere → miss → fail.
	miss := scoreAdvice(&AdviceExpected{ExpectAvoid: []string{"AP4", "AP7"}}, r)
	if miss.Passed {
		t.Error("expected fail (AP7 missing)")
	}
	if len(miss.Avoid.Missed) != 1 || miss.Avoid.Missed[0] != "AP7" {
		t.Errorf("avoid missed = %v, want [AP7]", miss.Avoid.Missed)
	}

	// Right cite, wrong bucket → miss (bucket placement matters).
	wrongBucket := scoreAdvice(&AdviceExpected{ExpectPrefer: []string{"AP4"}}, r)
	if wrongBucket.Passed {
		t.Error("AP4 is in avoid, not prefer — expected fail")
	}

	// Empty expectations → trivially pass.
	if !scoreAdvice(&AdviceExpected{}, r).Passed {
		t.Error("no expectations should pass")
	}
}
