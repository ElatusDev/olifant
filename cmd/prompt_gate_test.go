package cmd

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gateFixture builds a minimal platform tree (kb + one repo file) and chdirs
// into it so findUp resolves. Returns the platform root.
func gateFixture(t *testing.T) string {
	t.Helper()
	platformRoot := t.TempDir()
	kbRoot := filepath.Join(platformRoot, "knowledge-base")

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(kbRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "kb\n")
	write("decisions/log.md", "### D210 — a decision\n")
	write("anti-patterns/catalog.md", "### AP164 — a trap\n")
	t.Chdir(platformRoot)
	return platformRoot
}

func TestPromptCheck_CleanDocPasses(t *testing.T) {
	root := gateFixture(t)
	doc := filepath.Join(root, "doc.md")
	if err := os.WriteFile(doc, []byte("Per D210 and AP164.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := promptCheck([]string{"-no-record", doc}); code != 0 {
		t.Errorf("clean doc: exit %d, want 0", code)
	}
}

func TestPromptCheck_FabricatedCiteFails(t *testing.T) {
	root := gateFixture(t)
	doc := filepath.Join(root, "doc.md")
	if err := os.WriteFile(doc, []byte("Grounded in D9999.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := promptCheck([]string{"-no-record", doc}); code != 1 {
		t.Errorf("fabricated cite: exit %d, want 1", code)
	}
}

func TestPromptCheck_UsageErrors(t *testing.T) {
	gateFixture(t)
	if code := promptCheck([]string{"-no-record"}); code != 2 {
		t.Errorf("missing arg: exit %d, want 2", code)
	}
	if code := promptCheck([]string{"-no-record", "nope.md"}); code != 2 {
		t.Errorf("missing doc: exit %d, want 2", code)
	}
}

func TestPromptContext_RetrievalOnlyAgainstFakeStack(t *testing.T) {
	// genJSON is irrelevant: the context path must never hit /api/generate.
	fakeStack(t, `unused`)
	if code := promptContext([]string{"-no-record", "-scope", "backend", "goal text"}); code != 0 {
		t.Errorf("prompt context: exit %d, want 0", code)
	}
}

func TestPromptContext_MissingGoal(t *testing.T) {
	if code := promptContext([]string{"-no-record"}); code != 2 {
		t.Errorf("missing goal: exit %d, want 2", code)
	}
}

func TestPrompt_DispatchesNewVerbs(t *testing.T) {
	if code := Prompt([]string{"context"}); code != 2 { // missing goal
		t.Errorf("prompt context dispatch: exit %d, want 2", code)
	}
	if code := Prompt([]string{"check"}); code != 2 { // missing doc
		t.Errorf("prompt check dispatch: exit %d, want 2", code)
	}
}

// TestContextAndCheckPathsImportNoSynth is the AC1/D-OP1 structural assertion:
// the retrieval-context and cite-gate code paths must not import the
// synthesizer or the claude CLI seam.
func TestContextAndCheckPathsImportNoSynth(t *testing.T) {
	banned := []string{"internal/synth", "internal/claudecli"}
	files := []string{
		"prompt_context.go",
		"prompt_check.go",
		"retrieve.go",
		"../internal/prompt/context.go",
		"../internal/promptgate/resolver.go",
		"../internal/promptgate/check.go",
	}
	fset := token.NewFileSet()
	for _, f := range files {
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range parsed.Imports {
			for _, b := range banned {
				if strings.Contains(imp.Path.Value, b) {
					t.Errorf("%s imports %s — D-OP1 violated", f, imp.Path.Value)
				}
			}
		}
	}
}
