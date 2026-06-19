package validate

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

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/synth"
)

// fakeSynth returns canned text per call, cycling through `outs`.
type fakeSynth struct {
	outs []string
	n    int
}

func (f *fakeSynth) Generate(ctx context.Context, req synth.Request) (*synth.Response, error) {
	out := f.outs[min(f.n, len(f.outs)-1)]
	f.n++
	return &synth.Response{Text: out, EvalCount: 5, EvalDuration: 1e9}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestExtractVerdict(t *testing.T) {
	r := &Result{RawJSON: `{"validate":{"overall_verdict":"validated","proceed":"merge"}}`}
	v, p := r.ExtractVerdict()
	if v != "validated" || p != "merge" {
		t.Errorf("ExtractVerdict = (%q,%q), want (validated,merge)", v, p)
	}

	bad := &Result{RawJSON: "not json"}
	if v, p := bad.ExtractVerdict(); v != "" || p != "" {
		t.Errorf("invalid json = (%q,%q), want empty", v, p)
	}
}

func TestJSONToYAML(t *testing.T) {
	out, ok := jsonToYAML(`{"a":1,"b":"x"}`)
	if !ok {
		t.Fatal("valid json should report ok")
	}
	if !strings.Contains(out, "a: 1") || !strings.Contains(out, "b: x") {
		t.Errorf("yaml = %q", out)
	}

	raw, ok := jsonToYAML("not json")
	if ok {
		t.Error("invalid json should report not-ok")
	}
	if raw != "not json" {
		t.Errorf("invalid passthrough = %q", raw)
	}
}

func TestResolveDiff(t *testing.T) {
	if _, err := ResolveDiff("", ""); err == nil {
		t.Error("empty ref should error")
	}

	// File path → reads contents.
	p := filepath.Join(t.TempDir(), "patch.diff")
	if err := os.WriteFile(p, []byte("diff --git a/x b/x\n+hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := ResolveDiff(p, "")
	if err != nil {
		t.Fatalf("ResolveDiff file: %v", err)
	}
	if !strings.Contains(body, "diff --git") {
		t.Errorf("file diff body = %q", body)
	}
}

func TestResolveDiff_GitRange(t *testing.T) {
	// Build a tiny git repo with one commit and assert a range diff resolves.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "first")

	// `git diff HEAD` is empty on a clean tree → falls through to `git show HEAD`.
	body, err := ResolveDiff("HEAD", dir)
	if err != nil {
		t.Fatalf("ResolveDiff git: %v", err)
	}
	if !strings.Contains(body, "first") && !strings.Contains(body, "f.txt") {
		t.Errorf("git show body unexpected:\n%s", body)
	}
}

func TestRun_NoValidator_SingleSynthCall(t *testing.T) {
	fs := &fakeSynth{outs: []string{`{"validate":{"overall_verdict":"validated","proceed":"merge"}}`}}
	res, err := Run(context.Background(), Config{
		Claim:       "added a test",
		Diff:        "diff --git a/x b/x",
		Synthesizer: "m",
		Synth:       fs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ValidateAttempts != 1 {
		t.Errorf("attempts = %d, want 1 (no validator → no retry)", res.ValidateAttempts)
	}
	if !res.JSONValid || res.RetrievedCount != 0 {
		t.Errorf("result = %+v", res)
	}
	if v, p := res.ExtractVerdict(); v != "validated" || p != "merge" {
		t.Errorf("verdict = (%q,%q)", v, p)
	}
}

// buildValidatorKB writes a minimal dictionary so NewCiteValidator loads terms.
func buildValidatorKB(t *testing.T) *challenge.CiteValidator {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	dictFile := filepath.Join(kb, "dictionary", "backend", "domain.yaml")
	if err := os.MkdirAll(filepath.Dir(dictFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dictFile, []byte("- term: SB-04\n- term: D154\n- term: AP3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := challenge.NewCiteValidator(root, kb)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}
	return v
}

func TestAllDictionaryTerms_And_Schema(t *testing.T) {
	v := buildValidatorKB(t)

	terms := allDictionaryTerms(v)
	if len(terms) == 0 {
		t.Fatal("expected dictionary terms loaded")
	}
	std := filterByPattern(terms, ReStandardID)
	if len(std) == 0 {
		t.Errorf("expected at least one standard-shaped ID in %v", terms)
	}

	// Non-nil validator → dynamic schema (differs from the static base).
	schema := BuildValidateSchema(v, []string{"backend"})
	if schema == nil {
		t.Fatal("schema nil")
	}
	if _, ok := schema["properties"].(map[string]interface{})["validate"]; !ok {
		t.Errorf("dynamic schema missing validate property")
	}
}

func TestRun_WithValidator_RetrievesAndRetries(t *testing.T) {
	v := buildValidatorKB(t)

	// Fake Ollama (embed) + Chroma (collections + query) for the retrieval path.
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer oll.Close()
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"c1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["doc"]],"metadatas":[[{"source":"patterns/backend.md"}]],"distances":[[0.1]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer chr.Close()

	// Blocker-inducing output (empty claims_parsed) forces the retry loop.
	blocker := `{"validate":{"claim_summary":"x","claims_parsed":[],"claim_assessments":[],"standards_satisfied":[],"standards_violated":[],"overall_verdict":"failed","proceed":"block"}}`
	fs := &fakeSynth{outs: []string{blocker, blocker}}

	res, err := Run(context.Background(), Config{
		Claim:              "did the thing",
		Diff:               "diff --git a/x b/x",
		OllamaURL:          oll.URL,
		ChromaURL:          chr.URL,
		Embedder:           "bge-m3",
		Synthesizer:        "m",
		Scopes:             []string{"backend"},
		Validator:          v,
		MaxValidateRetries: 1,
		Synth:              fs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieval hits with validator wired")
	}
	if res.ValidateAttempts != 2 {
		t.Errorf("attempts = %d, want 2 (one retry on blocker)", res.ValidateAttempts)
	}
	if len(res.FirstAttemptViolations) == 0 {
		t.Error("expected first-attempt violations snapshot")
	}
}
