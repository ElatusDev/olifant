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

	"github.com/ElatusDev/olifant/internal/ollama"
)

// The challenge JSON the fake synthesizer "produces" for the eval case.
const evalChallengeOut = `{"challenge":{"request":"add a tenant scoped invoice entity","verdict":"VALID","proceed":"proceed_directly","confirms":[{"claim":"ok","cites":["SB-04"]}],"applicable_rules":{"standards":["D154"],"patterns":[],"anti_patterns_to_avoid":[],"decisions_to_honor":[]}}}`

func TestRun_EndToEnd(t *testing.T) {
	// Fake Ollama: version + embed + generate (returns the challenge JSON).
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
			resp := map[string]interface{}{"response": evalChallengeOut, "done": true, "eval_count": 5, "eval_duration": 1e9}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer oll.Close()

	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_, _ = w.Write([]byte(`{"nanosecond heartbeat":1}`))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"c1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc"]],"metadatas":[[{"source":"patterns/backend.md","scope":"backend"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer chr.Close()

	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	t.Setenv("OLIFANT_OLLAMA_URL", oll.URL)
	t.Setenv("OLIFANT_CHROMA_URL", chr.URL)
	t.Setenv("OLIFANT_EMBEDDER", "bge-m3")
	t.Setenv("OLIFANT_SYNTHESIZER", "synth-m")

	// Minimal KB so the cite-validator initialises (grounding path).
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	dict := filepath.Join(kb, "dictionary", "backend", "domain.yaml")
	if err := os.MkdirAll(filepath.Dir(dict), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dict, []byte("- term: SB-04\n- term: D154\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	suite := &Suite{
		SuiteID: "smoke",
		Cases: []Case{
			{
				ID:      "case-1",
				Scope:   []string{"backend"},
				Request: "add a tenant scoped invoice entity",
				Expected: &Expected{
					Verdict:       "VALID",
					MustCiteAnyOf: []string{"SB-04"},
				},
			},
		},
	}

	report, err := Run(context.Background(), RunConfig{
		Suite:        suite,
		PlatformRoot: root,
		KBRoot:       kb,
		OutDir:       out,
		Verbose:      true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.TotalCases != 1 {
		t.Errorf("TotalCases = %d, want 1", report.TotalCases)
	}
	if len(report.Cases) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(report.Cases))
	}
	cr := report.Cases[0]
	if cr.Verdict != "VALID" {
		t.Errorf("case verdict = %q, want VALID", cr.Verdict)
	}
	if report.GradedPassRate == nil {
		t.Error("expected a graded pass rate (case carried Expected)")
	}
	// report.yaml + per-case output written under the run dir.
	if _, err := os.Stat(filepath.Join(out, report.RunID, "report.yaml")); err != nil {
		t.Errorf("report.yaml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, report.RunID, "case-1", "output.yaml")); err != nil {
		t.Errorf("case output.yaml not written: %v", err)
	}
}

func TestRun_RequestBuildFailureRecorded(t *testing.T) {
	// A case with neither file nor request → build error recorded, no crash.
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	out := t.TempDir()
	report, err := Run(context.Background(), RunConfig{
		Suite:        &Suite{SuiteID: "s", Cases: []Case{{ID: "bad"}}},
		PlatformRoot: t.TempDir(),
		KBRoot:       t.TempDir(),
		OutDir:       out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Cases) != 1 || report.Cases[0].Error == "" {
		t.Errorf("expected recorded request-build error, got %+v", report.Cases)
	}
}
