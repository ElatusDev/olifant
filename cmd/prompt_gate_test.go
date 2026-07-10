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

// TestPromptCheck_KBRootPin covers olifant#79 (CS-F5): -kb-root (and the
// OLIFANT_KB_ROOT env) resolve bare IDs against the PINNED tree — the
// branch-new-artifact case — while repo path cites keep resolving via the
// real platform root, and the default stays findUp.
func TestPromptCheck_KBRootPin(t *testing.T) {
	platformRoot := gateFixture(t) // findUp tree: knows D210/AP164 only

	// A repo file under the REAL platform root (repo cites must keep working
	// under a pin).
	repoFile := filepath.Join(platformRoot, "core-api", "src", "Foo.java")
	if err := os.MkdirAll(filepath.Dir(repoFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoFile, []byte("class Foo {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The pinned tree: a branch worktree that ALSO defines D999 (the
	// "artifact minted on this branch" case).
	pinned := t.TempDir()
	for rel, body := range map[string]string{
		"decisions/log.md":         "### D210 — a decision\n### D999 — minted on this branch\n",
		"anti-patterns/catalog.md": "### AP164 — a trap\n",
	} {
		p := filepath.Join(pinned, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	doc := filepath.Join(platformRoot, "doc.md")
	if err := os.WriteFile(doc, []byte("Per D999 (new) and core-api/src/Foo.java.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default (findUp tree lacks D999) → fail: the exact pre-fix friction.
	if code := promptCheck([]string{"-no-record", doc}); code != 1 {
		t.Errorf("default should fail on the branch-new ID: exit %d, want 1", code)
	}
	// Pinned via flag → both cites resolve in ONE invocation (AC4 shape).
	if code := promptCheck([]string{"-no-record", "-kb-root", pinned, doc}); code != 0 {
		t.Errorf("pinned check: exit %d, want 0", code)
	}
	// Pinned via env → same.
	t.Setenv("OLIFANT_KB_ROOT", pinned)
	if code := promptCheck([]string{"-no-record", doc}); code != 0 {
		t.Errorf("env-pinned check: exit %d, want 0", code)
	}
}
