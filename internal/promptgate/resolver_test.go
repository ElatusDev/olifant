package promptgate

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture builds a minimal platform+kb tree: Layer-1 sources carrying recent
// artifact IDs, an old-era dictionary term, a real file for path cites, and a
// corpus manifest tracking decisions/log.md.
func fixture(t *testing.T) (platformRoot, kbRoot string) {
	t.Helper()
	platformRoot = t.TempDir()
	kbRoot = filepath.Join(platformRoot, "knowledge-base")

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

	write("decisions/log.md", "### D210 — promote the thing\nSee also AP150.\n")
	write("anti-patterns/catalog.md", "### AP164 — the trap\n")
	write("standards/CODE-QUALITY-STANDARD.md", "Rule B-12 applies.\n")
	write("patterns/backend.md", "Pattern text, no ids.\n")
	write("dictionary/universal/domain.yaml", "- term: D50\n")
	write("corpus/v1/manifest.yaml",
		"sources:\n  - path: decisions/log.md\n    sha: indexed-sha-aaa\n    scope: universal\n    doc_type: decision\n    chunks: 1\n")

	if err := os.MkdirAll(filepath.Join(platformRoot, "core-api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformRoot, "core-api", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return platformRoot, kbRoot
}

func newFixtureResolver(t *testing.T) *Resolver {
	t.Helper()
	platformRoot, kbRoot := fixture(t)
	r, err := NewResolver(platformRoot, kbRoot)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

func TestNewResolver_ScansLayer1ForBareIDs(t *testing.T) {
	r := newFixtureResolver(t)

	cases := []struct {
		cite    string
		verdict Verdict
		source  string
	}{
		{"D210", VerdictResolved, "decisions/log.md"},  // post-dictionary-era decision
		{"AP150", VerdictResolved, "decisions/log.md"}, // mentioned, not defined — still citable
		{"AP164", VerdictResolved, "anti-patterns/catalog.md"},
		{"B-12", VerdictResolved, "standards/CODE-QUALITY-STANDARD.md"},
		{"D9999", VerdictUnresolved, ""},
		{"AP9999", VerdictUnresolved, ""},
	}
	for _, c := range cases {
		got := r.Resolve(c.cite)
		if got.Verdict != c.verdict || got.Source != c.source {
			t.Errorf("Resolve(%q) = {%s %q}, want {%s %q}", c.cite, got.Verdict, got.Source, c.verdict, c.source)
		}
	}
}

func TestResolve_DictionaryTermDelegatesToValidator(t *testing.T) {
	r := newFixtureResolver(t)
	got := r.Resolve("D50")
	if got.Verdict != VerdictResolved {
		t.Errorf("Resolve(D50) = %s, want resolved (dictionary term)", got.Verdict)
	}
}

func TestResolve_PathCites(t *testing.T) {
	r := newFixtureResolver(t)
	if got := r.Resolve("core-api/README.md"); got.Verdict != VerdictResolved {
		t.Errorf("real path = %s, want resolved", got.Verdict)
	}
	if got := r.Resolve("core-api/missing.md"); got.Verdict != VerdictUnresolved {
		t.Errorf("missing path = %s, want unresolved", got.Verdict)
	}
}

func TestResolve_EmptyCiteUnresolved(t *testing.T) {
	r := newFixtureResolver(t)
	for _, cite := range []string{"", "   "} {
		if got := r.Resolve(cite); got.Verdict != VerdictUnresolved {
			t.Errorf("Resolve(%q) = %s, want unresolved", cite, got.Verdict)
		}
	}
}

// White-box: staleness fires only when both manifest and working tree know the
// source and disagree; absence of either side is not staleness.
func TestOverlayStale_RequiresBothSidesToDisagree(t *testing.T) {
	r := newFixtureResolver(t)
	r.artifactIDs["D777"] = "decisions/log.md"

	// Fixture dir is not a git repo → liveSHAs empty → resolved.
	if got := r.Resolve("D777"); got.Verdict != VerdictResolved {
		t.Errorf("no live sha: %s, want resolved", got.Verdict)
	}

	r.liveSHAs["decisions/log.md"] = "live-sha-bbb" // differs from indexed-sha-aaa
	if got := r.Resolve("D777"); got.Verdict != VerdictStale {
		t.Errorf("sha mismatch: %s, want stale", got.Verdict)
	}

	r.liveSHAs["decisions/log.md"] = "indexed-sha-aaa"
	if got := r.Resolve("D777"); got.Verdict != VerdictResolved {
		t.Errorf("sha match: %s, want resolved", got.Verdict)
	}
}

func TestResolve_KBPathCiteGetsStalenessOverlay(t *testing.T) {
	r := newFixtureResolver(t)
	r.liveSHAs["decisions/log.md"] = "live-sha-bbb"

	got := r.Resolve("knowledge-base/decisions/log.md")
	if got.Verdict != VerdictStale || got.Source != "decisions/log.md" {
		t.Errorf("kb path cite = {%s %q}, want {stale decisions/log.md}", got.Verdict, got.Source)
	}
}

func TestManifestSourceSHAs_MissingOrMalformed(t *testing.T) {
	if got := manifestSourceSHAs(nil); len(got) != 0 {
		t.Errorf("empty manifest bytes: %d entries, want 0", len(got))
	}
	if got := manifestSourceSHAs([]byte(":\tnot yaml")); len(got) != 0 {
		t.Errorf("malformed manifest: %d entries, want 0", len(got))
	}
}

func TestKnownArtifactCount(t *testing.T) {
	r := newFixtureResolver(t)
	// D210, AP150, AP164, B-12 from the fixture Layer-1 files.
	if n := r.KnownArtifactCount(); n != 4 {
		t.Errorf("KnownArtifactCount = %d, want 4", n)
	}
}
