package promptgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ElatusDev/olifant/internal/kbtree"
)

// fixtureKBRepo commits a small KB tree (Layer-1 sources + a dictionary + a
// corpus manifest) into a temp git repo and returns the dir. HEAD == the
// working tree, so an fsTree checkout and a gitTree of HEAD see the same bytes.
func fixtureKBRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"decisions/log.md":              "### D210: a decision\n### D227: roots\n",
		"anti-patterns/catalog.md":      "### AP164: an anti-pattern\n",
		"standards/CODE-QUALITY.md":     "See D210.\n",
		"patterns/backend.md":           "pattern text\n",
		"dictionary/backend/terms.yaml": "- term: tenant\n  synonyms: [org]\n",
		"corpus/v1/manifest.yaml":       "sources: []\n",
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
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "kb")
	return dir
}

// AC4: over an identical tree, a resolver built from the working-tree checkout
// (fsTree) and one built from that tree's git ref (gitTree) return identical
// verdicts for every cite — the byte-for-byte equivalence the pinned-worktree
// path is a superset of (olifant#90).
func TestResolverGitRefEquivalence(t *testing.T) {
	dir := fixtureKBRepo(t)

	fsR, err := NewResolver(dir, dir) // platformRoot == kbRoot for the fixture
	if err != nil {
		t.Fatal(err)
	}
	gt, err := kbtree.Git(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gitR, err := NewResolverTree(dir, gt)
	if err != nil {
		t.Fatal(err)
	}

	if fsR.KnownArtifactCount() != gitR.KnownArtifactCount() {
		t.Fatalf("known-artifact count fs=%d git=%d", fsR.KnownArtifactCount(), gitR.KnownArtifactCount())
	}
	for _, cite := range []string{
		"D210", "D227", "AP164", // bare IDs from the Layer-1 scan
		"tenant", "org", // dictionary term + synonym
		"decisions/log.md",                   // KB path cite
		"knowledge-base/patterns/backend.md", // prefixed KB path cite
		"D9999", "nope",                      // unresolved
	} {
		a := fsR.Resolve(cite)
		b := gitR.Resolve(cite)
		if a != b {
			t.Errorf("Resolve(%q): fs=%+v git=%+v", cite, a, b)
		}
	}
}

// A git-ref resolver must not read the working tree at all: point it at a git
// ref while the fs KB root is a non-existent path, and resolution still works —
// proving no silent working-tree fallback (olifant#90 D-GR4).
func TestGitRefNoFilesystemFallback(t *testing.T) {
	dir := fixtureKBRepo(t)
	gt, err := kbtree.Git(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// platformRoot points nowhere real; only the gitTree should serve KB cites.
	gitR, err := NewResolverTree(filepath.Join(dir, "does-not-exist"), gt)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitR.Resolve("D210"); got.Verdict != VerdictResolved {
		t.Errorf("D210 via git-ref with a dead fs root = %+v; want resolved", got)
	}
	if got := gitR.Resolve("tenant"); got.Verdict != VerdictResolved {
		t.Errorf("dictionary term via git-ref = %+v; want resolved", got)
	}
}
