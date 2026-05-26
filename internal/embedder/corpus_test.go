package embedder

import (
	"os"
	"path/filepath"
	"testing"
)

const fixtureYAML = `
- id: SYM-aaa
  text: 'First sentence with tags.'
  source: doc-a.md
  line: 10
  tags:
    concern:
      - security
      - testing
    language: markdown
    scope: backend
    semantic_role: constraint
    syntactic_form: affirmation
- id: SYM-bbb
  text: 'Second sentence, single concern as scalar.'
  source: doc-a.md
  line: 11
  tags:
    concern: ci
    language: markdown
    scope: backend
    semantic_role: definition
    syntactic_form: affirmation
- id: SYM-ccc
  text: ''
  source: doc-a.md
  line: 99
- id: ''
  text: 'No id; must be skipped.'
  source: doc-a.md
  line: 100
`

const fixtureYAMLOther = `
- id: SYM-ddd
  text: 'Third sentence in another file.'
  source: doc-b.md
  line: 5
  tags:
    concern:
      - persistence
    language: java
    scope: backend
    semantic_role: example
    syntactic_form: affirmation
`

func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if name == "" {
		name = "prose.yaml"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

func TestLoadProse_SingleFile(t *testing.T) {
	dir := writeFixture(t, "a.yaml", fixtureYAML)

	ss, err := LoadProse(dir)
	if err != nil {
		t.Fatalf("LoadProse: %v", err)
	}
	if got, want := len(ss), 2; got != want {
		t.Fatalf("loaded %d sentences, want %d (empty text + empty id must drop)", got, want)
	}

	first := ss[0]
	if first.ID != "SYM-aaa" || first.Scope != "backend" || first.SemanticRole != "constraint" {
		t.Fatalf("axis flatten broken: %+v", first)
	}
	if got, want := len(first.Concerns), 2; got != want {
		t.Fatalf("concerns from sequence: got %d, want %d (%v)", got, want, first.Concerns)
	}
	if first.Concerns[0] != "security" {
		t.Fatalf("concerns[0]=%q, want security", first.Concerns[0])
	}

	second := ss[1]
	if got, want := len(second.Concerns), 1; got != want || second.Concerns[0] != "ci" {
		t.Fatalf("scalar concern -> []string=%v", second.Concerns)
	}
}

func TestLoadProse_MultiFileDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(fixtureYAMLOther), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(fixtureYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, err := LoadProse(dir)
	if err != nil {
		t.Fatalf("LoadProse: %v", err)
	}
	if len(ss) != 3 {
		t.Fatalf("len=%d, want 3", len(ss))
	}
	// Sorted by source + line.
	wantOrder := []string{"SYM-aaa", "SYM-bbb", "SYM-ddd"}
	for i, w := range wantOrder {
		if ss[i].ID != w {
			t.Errorf("ss[%d].ID=%q, want %q", i, ss[i].ID, w)
		}
	}
}

func TestScopeIndex(t *testing.T) {
	ss := []Sentence{
		{ID: "1", Scope: "backend"},
		{ID: "2", Scope: "webapp"},
		{ID: "3", Scope: "backend"},
		{ID: "4", Scope: ""}, // missing scope must drop
	}
	idx := ScopeIndex(ss)
	if got, want := len(idx["backend"]), 2; got != want {
		t.Errorf("backend count = %d, want %d", got, want)
	}
	if got, want := len(idx["webapp"]), 1; got != want {
		t.Errorf("webapp count = %d, want %d", got, want)
	}
	if _, present := idx[""]; present {
		t.Errorf("empty-scope bucket must not be created")
	}
}

func TestLoadProse_NestedDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "deep.yaml"), []byte(fixtureYAMLOther), 0o644); err != nil {
		t.Fatal(err)
	}
	ss, err := LoadProse(dir)
	if err != nil {
		t.Fatalf("LoadProse: %v", err)
	}
	if len(ss) != 1 || ss[0].ID != "SYM-ddd" {
		t.Fatalf("nested walk broken: got %+v", ss)
	}
}
