package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// fakeStack stands up fake Ollama + Chroma servers, points OLIFANT_* env at
// them (synth backend = ollama), and returns nothing — callers just invoke the
// command. `genJSON` is what /api/generate "produces".
func fakeStack(t *testing.T, genJSON string) {
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"response": genJSON, "done": true, "eval_count": 5, "eval_duration": 1e9,
			})
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
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc"]],"metadatas":[[{"source":"patterns/backend.md","scope":"backend"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)

	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")
	t.Setenv("OLIFANT_OLLAMA_URL", oll.URL)
	t.Setenv("OLIFANT_CHROMA_URL", chr.URL)
	t.Setenv("OLIFANT_EMBEDDER", "bge-m3")
	t.Setenv("OLIFANT_SYNTHESIZER", "synth-m")
}

const planJSON = `{"plan":{"goal":"add invoices","scope":["backend"],"steps":[{"id":"step_01","name":"S","description":"d","depends_on":[],"expected_output":{"type":"object","fields":["x"]}}]}}`

const challengeJSON = `{"challenge":{"request":"add a tenant scoped invoice entity","verdict":"VALID","proceed":"proceed_directly","confirms":[],"applicable_rules":{"standards":[],"patterns":[],"anti_patterns_to_avoid":[],"decisions_to_honor":[]}}}`

const validateJSON = `{"validate":{"claim_summary":"x","claims_parsed":[{"id":"c1","text":"added a test"}],"claim_assessments":[{"claim_id":"c1","verdict":"evidenced","evidence":"added test method in x","cites":["x#L1-L2"]}],"standards_satisfied":[],"standards_violated":[],"overall_verdict":"validated","proceed":"merge"}}`

func TestPromptBuild_Integration(t *testing.T) {
	fakeStack(t, planJSON)
	out := t.TempDir()
	code := promptBuild([]string{"-out", out, "-no-record", "add a tenant scoped invoice entity"})
	if code != 0 {
		t.Errorf("promptBuild = %d, want 0", code)
	}
	// Missing goal → usage 2.
	if code := promptBuild([]string{"-no-record"}); code != 2 {
		t.Errorf("promptBuild(no goal) = %d, want 2", code)
	}
}

func TestChallenge_Integration(t *testing.T) {
	fakeStack(t, challengeJSON)
	code := Challenge([]string{"-no-record", "add a tenant scoped invoice entity"})
	if code != 0 {
		t.Errorf("Challenge = %d, want 0", code)
	}
}

func TestValidate_Integration(t *testing.T) {
	fakeStack(t, validateJSON)
	patch := filepath.Join(t.TempDir(), "change.diff")
	_ = os.WriteFile(patch, []byte("diff --git a/x b/x\n+added line\n"), 0o644)
	code := Validate([]string{
		"-no-record", "-no-retrieval",
		"-claim-text", "added a test",
		"-diff", patch,
	})
	if code != 0 {
		t.Errorf("Validate = %d, want 0", code)
	}
}

func TestEvalRun_Integration(t *testing.T) {
	fakeStack(t, challengeJSON)
	suite := filepath.Join(t.TempDir(), "suite.yaml")
	_ = os.WriteFile(suite, []byte("suite_id: smoke\ncases:\n  - id: c1\n    scope: [backend]\n    request: add a tenant scoped invoice entity\n"), 0o644)
	out := t.TempDir()
	code := evalRun([]string{"-suite", suite, "-out", out})
	if code != 0 {
		t.Errorf("evalRun = %d, want 0", code)
	}
}

func TestCorpusIndex_Integration(t *testing.T) {
	fakeStack(t, "")
	corpusDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(corpusDir, "backend.ndjson"),
		[]byte(`{"chunk_id":"1","source":"x","scope":"backend","doc_type":"code","body":"b"}`+"\n"), 0o644)
	// -kb-root explicit so the test doesn't depend on findUp locating a real
	// knowledge-base ancestor (absent in CI's standalone checkout).
	code := corpusIndex([]string{"-kb-root", t.TempDir(), "-corpus-dir", corpusDir})
	if code != 0 {
		t.Errorf("corpusIndex = %d, want 0", code)
	}
}
