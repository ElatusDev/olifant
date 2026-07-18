package kbtree

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureRepo creates a temp git repo with a small KB-shaped tree, commits it,
// and returns the dir. HEAD then equals the working tree.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"decisions/log.md":              "### D227: roots\n### D231: pin\n",
		"anti-patterns/catalog.md":      "AP171 stale checkout\n",
		"standards/CODE-QUALITY.md":     "rule\n",
		"dictionary/backend/terms.yaml": "- term: tenant\n",
		"dictionary/apex.yaml":          "- term: platform\n",
		"corpus/v1/manifest.yaml":       "sources: {}\n",
	}
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "fixture")
	return dir
}

func TestFSTreeBasics(t *testing.T) {
	dir := fixtureRepo(t)
	fs := FS(dir)

	body, err := fs.ReadFile("decisions/log.md")
	if err != nil || string(body) == "" {
		t.Fatalf("ReadFile decisions/log.md = %q, %v", body, err)
	}
	if !fs.Exists("standards/CODE-QUALITY.md") {
		t.Error("Exists standards/CODE-QUALITY.md = false")
	}
	if fs.Exists("nope/missing.md") {
		t.Error("Exists on missing file = true")
	}
	if fs.Exists("decisions") { // a dir, not a file
		t.Error("Exists on a dir = true")
	}

	glob, _ := fs.Glob("standards/*.md")
	if len(glob) != 1 || glob[0] != "standards/CODE-QUALITY.md" {
		t.Errorf("Glob standards/*.md = %v", glob)
	}
	list, _ := fs.List("dictionary")
	sort.Strings(list)
	want := []string{"dictionary/apex.yaml", "dictionary/backend/terms.yaml"}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("List dictionary = %v; want %v", list, want)
	}
	if len(fs.BlobSHAs()) == 0 {
		t.Error("BlobSHAs empty for a git checkout")
	}
}

func TestFSTreeEmptyRoot(t *testing.T) {
	fs := FS("")
	if fs.Exists("anything") {
		t.Error("empty-root Exists = true")
	}
	if g, _ := fs.Glob("*.md"); g != nil {
		t.Error("empty-root Glob non-nil")
	}
	if l, _ := fs.List("x"); l != nil {
		t.Error("empty-root List non-nil")
	}
	if len(fs.BlobSHAs()) != 0 {
		t.Error("empty-root BlobSHAs non-empty")
	}
}

func TestGitTreeBasics(t *testing.T) {
	dir := fixtureRepo(t)
	gt, err := Git(dir, "HEAD")
	if err != nil {
		t.Fatalf("Git(HEAD): %v", err)
	}
	body, err := gt.ReadFile("decisions/log.md")
	if err != nil || string(body) == "" {
		t.Fatalf("ReadFile = %q, %v", body, err)
	}
	if !gt.Exists("dictionary/backend/terms.yaml") || gt.Exists("nope.md") {
		t.Error("Exists wrong")
	}
	if _, err := gt.ReadFile("nope.md"); err == nil {
		t.Error("ReadFile(missing) = nil error")
	}
	glob, _ := gt.Glob("standards/*.md")
	if len(glob) != 1 || glob[0] != "standards/CODE-QUALITY.md" {
		t.Errorf("Glob = %v", glob)
	}
	list, _ := gt.List("dictionary")
	sort.Strings(list)
	if !reflect.DeepEqual(list, []string{"dictionary/apex.yaml", "dictionary/backend/terms.yaml"}) {
		t.Errorf("List = %v", list)
	}
	if len(gt.BlobSHAs()) != 6 {
		t.Errorf("BlobSHAs = %d; want 6", len(gt.BlobSHAs()))
	}
}

func TestGitTreeMissingRef(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := Git(dir, "no-such-ref"); err == nil {
		t.Error("Git(bad ref) = nil error; want hard error (no silent fallback)")
	}
	if _, err := Git(dir, ""); err == nil {
		t.Error("Git(empty ref) = nil error")
	}
}

// The AC4 foundation: over an identical committed tree, fsTree (the checkout)
// and gitTree (the ref's blobs) return byte-identical reads.
func TestFSGitEquivalence(t *testing.T) {
	dir := fixtureRepo(t)
	fs := FS(dir)
	gt, err := Git(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"decisions/log.md", "anti-patterns/catalog.md", "dictionary/backend/terms.yaml"} {
		a, _ := fs.ReadFile(rel)
		b, _ := gt.ReadFile(rel)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("ReadFile(%s) fs≠git: %q vs %q", rel, a, b)
		}
	}
	if !reflect.DeepEqual(fs.BlobSHAs(), gt.BlobSHAs()) {
		t.Errorf("BlobSHAs fs≠git:\n fs=%v\ngit=%v", fs.BlobSHAs(), gt.BlobSHAs())
	}
	fg, _ := fs.Glob("standards/*.md")
	gg, _ := gt.Glob("standards/*.md")
	sort.Strings(fg)
	sort.Strings(gg)
	if !reflect.DeepEqual(fg, gg) {
		t.Errorf("Glob fs≠git: %v vs %v", fg, gg)
	}
}

func TestGitRejectsDashPrefixedRef(t *testing.T) {
	for _, ref := range []string{"-", "--help", " --output=/tmp/x"} {
		if _, err := Git(t.TempDir(), ref); err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
			t.Errorf("Git(%q) should reject dash-prefixed ref, got err=%v", ref, err)
		}
	}
}
