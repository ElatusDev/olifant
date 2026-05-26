package embedder

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubClaudeBin builds a tiny Go program that emulates `claude --print
// --model <M> --output-format json --json-schema <S> -- <prompt>` enough
// for the paraphrase loop: extract the trailing `Sentence: ...` arg and
// echo back a JSON envelope whose structured_output wraps a
// {"paraphrase": "X"} payload, where X mutates the sentence in a
// detectable way (uppercased + " [PARA]" suffix). Honours an env var
// STUB_FAIL=1 to simulate a non-zero exit.
func stubClaudeBin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub not built on Windows")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "stub.go")
	bin := filepath.Join(dir, "stub")
	prog := `package main

import (
	"encoding/json"
	"os"
	"strings"
)

func main() {
	if os.Getenv("STUB_FAIL") == "1" {
		os.Stderr.WriteString("stub forced failure\n")
		os.Exit(2)
	}
	args := os.Args[1:]
	var prompt string
	for i := range args {
		if args[i] == "--" && i+1 < len(args) {
			prompt = args[i+1]
			break
		}
	}
	sentence := ""
	if idx := strings.Index(prompt, "Sentence: "); idx >= 0 {
		sentence = prompt[idx+len("Sentence: "):]
	}
	para := strings.ToUpper(sentence) + " [PARA]"
	structured, _ := json.Marshal(map[string]string{"paraphrase": para})
	env := map[string]interface{}{
		"result":            string(structured),
		"structured_output": json.RawMessage(structured),
		"is_error":          false,
		"subtype":           "success",
	}
	out, _ := json.Marshal(env)
	os.Stdout.Write(out)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build stub: %v\n%s", err, out)
	}
	return bin
}

// TestGenerate_HappyPath drives the full Generate loop against the stub.
func TestGenerate_HappyPath(t *testing.T) {
	bin := stubClaudeBin(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "triples.jsonl")

	triples := []Triple{
		{AnchorID: "SYM-A", Anchor: "Hello world.", NegativeID: "SYM-B", Negative: "Goodbye.", Scope: "backend", AnchorRole: "constraint", NegativeRole: "definition", SourcePath: "x.md"},
		{AnchorID: "SYM-C", Anchor: "Another claim.", NegativeID: "SYM-D", Negative: "Counter.", Scope: "webapp", AnchorRole: "example", NegativeRole: "constraint", SourcePath: "y.md"},
	}
	st, err := Generate(context.Background(), GenConfig{
		Triples:        triples,
		OutPath:        out,
		ClaudeBin:      bin,
		Model:          "opus",
		Concurrency:    1,
		PerCallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if st.Succeeded != 2 || st.Failed != 0 {
		t.Errorf("stats: %+v", st)
	}

	data, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL has %d lines, want 2", len(lines))
	}
	for _, ln := range lines {
		var row PairedRow
		if err := json.Unmarshal([]byte(ln), &row); err != nil {
			t.Fatalf("unmarshal row: %v: %q", err, ln)
		}
		if row.Positive == "" || !strings.HasSuffix(row.Positive, "[PARA]") {
			t.Errorf("expected stub paraphrase marker in row %s, got %q", row.AnchorID, row.Positive)
		}
		if row.AnchorID == "" || row.NegativeID == "" {
			t.Errorf("row missing IDs: %+v", row)
		}
	}
}

// TestGenerate_ResumeSkipsExisting verifies anchor IDs already on disk
// are skipped on the second invocation.
func TestGenerate_ResumeSkipsExisting(t *testing.T) {
	bin := stubClaudeBin(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "triples.jsonl")

	triples := []Triple{
		{AnchorID: "SYM-A", Anchor: "x.", NegativeID: "SYM-B", Negative: "y.", Scope: "backend", AnchorRole: "constraint", NegativeRole: "definition"},
	}
	cfg := GenConfig{
		Triples:        triples,
		OutPath:        out,
		ClaudeBin:      bin,
		Model:          "opus",
		Concurrency:    1,
		PerCallTimeout: 5 * time.Second,
		Resume:         true,
	}
	if _, err := Generate(context.Background(), cfg); err != nil {
		t.Fatalf("first run: %v", err)
	}
	st2, err := Generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if st2.Skipped != 1 || st2.Processed != 0 {
		t.Errorf("second run stats: %+v (want Skipped=1)", st2)
	}
}

// TestGenerate_RetryThenFail verifies the MaxRetries pathway.
func TestGenerate_RetryThenFail(t *testing.T) {
	bin := stubClaudeBin(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "triples.jsonl")

	t.Setenv("STUB_FAIL", "1")
	st, err := Generate(context.Background(), GenConfig{
		Triples: []Triple{
			{AnchorID: "SYM-A", Anchor: "x.", NegativeID: "SYM-B", Negative: "y.", Scope: "backend", AnchorRole: "constraint", NegativeRole: "definition"},
		},
		OutPath:        out,
		ClaudeBin:      bin,
		Model:          "opus",
		Concurrency:    1,
		PerCallTimeout: 5 * time.Second,
		MaxRetries:     1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if st.Failed != 1 || st.Succeeded != 0 {
		t.Errorf("expected 1 failure, got %+v", st)
	}
}

// TestAnchorIDsIn covers the regex used for the "artifact-ID retention" sanity signal.
func TestAnchorIDsIn(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"see AP3 + D139 — also SB-04 and AMS-02", []string{"AP3", "D139", "SB-04", "AMS-02"}},
		{"no artifact IDs here", nil},
		{"refs CI1, F42, IMF1, WA-W03, FM7, D-CC11", []string{"CI1", "F42", "IMF1", "WA-W03", "FM7", "D-CC11"}},
	}
	for _, c := range cases {
		got := anchorIDsIn(c.in)
		if !strSliceEq(got, c.want) {
			t.Errorf("anchorIDsIn(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func strSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
