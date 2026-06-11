package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/embedder"
)

// captureRunner records every exec.Command invocation for inspection.
type captureRunner struct {
	calls [][]string
	stub  string // path to /usr/bin/true-equivalent stub bin
}

func (c *captureRunner) fn(name string, args ...string) *exec.Cmd {
	c.calls = append(c.calls, append([]string{name}, args...))
	// Always return a cmd that succeeds (exit 0) so the wrapper sees a
	// happy-path response. The stub path is wired in setUp.
	return exec.Command(c.stub)
}

// trueStub creates a tiny exit-0 binary so the wrapper's cmd.Run() succeeds.
func trueStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "trustub.go")
	bin := filepath.Join(dir, "trustub")
	prog := `package main
func main() { }
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return bin
}

// writeTriples creates a non-empty triples.jsonl so the embedderTrain
// pre-flight check passes.
func writeTriples(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "triples.jsonl")
	if err := os.WriteFile(p, []byte(`{"anchor":"a","positive":"b","negative":"c"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// swapRunner replaces the package-level runner; tests reset on cleanup.
func swapRunner(t *testing.T, fn func(string, ...string) *exec.Cmd) {
	t.Helper()
	old := runner
	runner = fn
	t.Cleanup(func() { runner = old })
}

func TestEmbedderTrain_HappyPath(t *testing.T) {
	stub := trueStub(t)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	triples := writeTriples(t)
	rc := embedderTrain([]string{
		"--triples", triples,
		"--app", "internal/embedder/modal_app.py",
		"--volume", "olifant-train-v1",
		"--remote", "/embedder-v1/triples.jsonl",
	})
	if rc != 0 {
		t.Fatalf("embedderTrain rc=%d, want 0", rc)
	}
	if len(cap.calls) != 2 {
		t.Fatalf("expected 2 modal calls (put + run), got %d: %v", len(cap.calls), cap.calls)
	}

	// Call 1: modal volume put <volume> <triples> <remote> --force
	put := cap.calls[0]
	if put[0] != "modal" || put[1] != "volume" || put[2] != "put" {
		t.Errorf("call[0] not `modal volume put …`: %v", put)
	}
	if !contains(put, "olifant-train-v1") || !contains(put, triples) || !contains(put, "/embedder-v1/triples.jsonl") {
		t.Errorf("call[0] missing expected args: %v", put)
	}
	if !contains(put, "--force") {
		t.Errorf("call[0] missing --force: %v", put)
	}

	// Call 2: modal run internal/embedder/modal_app.py::train_full
	run := cap.calls[1]
	if run[0] != "modal" || run[1] != "run" {
		t.Errorf("call[1] not `modal run …`: %v", run)
	}
	wantTarget := "internal/embedder/modal_app.py::train_full"
	if !contains(run, wantTarget) {
		t.Errorf("call[1] target != %q: %v", wantTarget, run)
	}
}

func TestEmbedderTrain_DryRunRoutesToDryRunEntry(t *testing.T) {
	stub := trueStub(t)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)
	triples := writeTriples(t)

	rc := embedderTrain([]string{
		"--triples", triples, "--dry-run",
	})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	last := cap.calls[len(cap.calls)-1]
	target := last[len(last)-1]
	if !strings.HasSuffix(target, "::dry_run") {
		t.Errorf("dry-run did not target dry_run entry-point: %q", target)
	}
}

func TestEmbedderTrain_SkipUploadHasNoVolumePut(t *testing.T) {
	stub := trueStub(t)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)
	triples := writeTriples(t)

	rc := embedderTrain([]string{
		"--triples", triples, "--skip-upload",
	})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("expected 1 call (run only), got %d", len(cap.calls))
	}
	if cap.calls[0][1] != "run" {
		t.Errorf("expected `modal run`, got: %v", cap.calls[0])
	}
}

func TestEmbedderTrain_MissingTriplesFails(t *testing.T) {
	stub := trueStub(t)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	rc := embedderTrain([]string{"--triples", "/nonexistent/path/triples.jsonl"})
	if rc == 0 {
		t.Fatal("expected non-zero rc for missing triples file")
	}
	if len(cap.calls) != 0 {
		t.Errorf("expected no modal calls when triples missing, got: %v", cap.calls)
	}
}

func TestEmbedderPull_ConstructsModalVolumeGet(t *testing.T) {
	stub := trueStub(t)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	dest := t.TempDir()
	rc := embedderPull([]string{"--local", filepath.Join(dest, "model")})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("expected 1 modal call, got %d", len(cap.calls))
	}
	c := cap.calls[0]
	if c[0] != "modal" || c[1] != "volume" || c[2] != "get" {
		t.Errorf("expected modal volume get, got: %v", c)
	}
	if !contains(c, "--force") {
		t.Errorf("expected --force in: %v", c)
	}
}

func TestEmbedder_UnknownActionExitsTwo(t *testing.T) {
	rc := Embedder([]string{"frobnicate"})
	if rc != 2 {
		t.Errorf("unknown action rc=%d, want 2", rc)
	}
}

func TestEmbedderRecall_MissingQueriesFlagExitsTwo(t *testing.T) {
	if rc := embedderRecall(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestEmbedderRecall_UnsupportedPairExitsTwo(t *testing.T) {
	rc := embedderRecall([]string{"--queries", "whatever.yaml", "--baseline", "bert"})
	if rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

// payloadStub builds a binary that prints the given stdout payload, so
// recallCandidate's `modal run` capture sees a marker-delimited response.
func payloadStub(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "stub.go")
	bin := filepath.Join(dir, "stub")
	prog := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Print(`" + payload + "`) }\n"
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return bin
}

func TestRecallCandidate_UploadsThenRunsAndParses(t *testing.T) {
	payload := `===OLIFANT_RECALL_JSON===
{"queries":[{"query_id":"q01","hits":[{"sentence_id":"s1","source":"a.md","score":0.9}]}]}
===END_OLIFANT_RECALL_JSON===`
	stub := payloadStub(t, payload)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	queries := []embedder.Query{{ID: "q01", Text: "where?", ExpectedSource: "a.md"}}
	sents := []embedder.Sentence{{ID: "s1", Text: "here", Source: "a.md"}}

	results, rc := recallCandidate(queries, sents, 5, "modal", defaultModalApp, defaultVolumeName, false, t.TempDir())
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(cap.calls) != 3 {
		t.Fatalf("expected 3 modal calls (2 puts + run), got %d: %v", len(cap.calls), cap.calls)
	}
	if cap.calls[0][2] != "put" || cap.calls[1][2] != "put" {
		t.Errorf("first two calls not volume put: %v", cap.calls[:2])
	}
	if !contains(cap.calls[0], defaultRecallRemoteDir+"/sentences.jsonl") {
		t.Errorf("call[0] missing sentences remote: %v", cap.calls[0])
	}
	if !contains(cap.calls[1], defaultRecallRemoteDir+"/queries.jsonl") {
		t.Errorf("call[1] missing queries remote: %v", cap.calls[1])
	}
	run := cap.calls[2]
	if run[1] != "run" || !contains(run, defaultModalApp+"::recall_embed") {
		t.Errorf("call[2] not modal run recall_embed: %v", run)
	}
	if !contains(run, "--top-k") {
		t.Errorf("call[2] missing --top-k: %v", run)
	}
	if len(results) != 1 || results[0].HitAt != 1 {
		t.Errorf("results = %+v", results)
	}
}

func TestRecallCandidate_SkipUploadRunsOnly(t *testing.T) {
	payload := `===OLIFANT_RECALL_JSON===
{"queries":[{"query_id":"q01","hits":[]}]}
===END_OLIFANT_RECALL_JSON===`
	stub := payloadStub(t, payload)
	cap := &captureRunner{stub: stub}
	swapRunner(t, cap.fn)

	queries := []embedder.Query{{ID: "q01", Text: "where?", ExpectedSource: "a.md"}}
	_, rc := recallCandidate(queries, nil, 5, "modal", defaultModalApp, defaultVolumeName, true, t.TempDir())
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(cap.calls) != 1 || cap.calls[0][1] != "run" {
		t.Errorf("expected single modal run, got: %v", cap.calls)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
