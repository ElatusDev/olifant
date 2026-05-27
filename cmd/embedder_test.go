package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
