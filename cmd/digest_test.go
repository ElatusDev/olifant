package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// digestKB extends the hermetic KB tree with the Layer-1 sources the
// promptgate resolver scans, plus a digestible artifact. Returns the
// artifact path.
func digestKB(t *testing.T, kb string) string {
	t.Helper()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(kb, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("decisions/log.md", "### D210 — the ratified thing\n")
	write("anti-patterns/catalog.md", "### AP164 — the trap\n")
	write("dictionary/universal/domain.yaml", "- term: sample\n")
	doc := filepath.Join(kb, "architecture", "target.md")
	write("architecture/target.md", strings.Repeat("Long design prose about D210.\n", 30))
	return doc
}

const digestBody = "The target doc explains the design.\n\n- It is grounded in D210.\n- The rest is expandable detail; read the source for depth.\n- This digest exists so a session does not need the whole document.\n"

func TestDigest_HappyPathAndCache(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, digestBody)
	doc := digestKB(t, kb)

	if code := Digest([]string{doc}); code != 0 {
		t.Fatalf("digest = %d, want 0", code)
	}

	// Cache populated under $HOME (redirected), never under the KB.
	home, _ := os.UserHomeDir()
	entries, err := os.ReadDir(filepath.Join(home, ".olifant", "digests"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("cache dir empty: %v", err)
	}

	// Turn record written with a digest block.
	turns, err := os.ReadDir(filepath.Join(kb, "short-term", "turns"))
	if err != nil || len(turns) == 0 {
		t.Fatalf("no turn record: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(kb, "short-term", "turns", turns[0].Name()))
	if !strings.Contains(string(raw), "subcommand: digest") || !strings.Contains(string(raw), "source_sha:") {
		t.Errorf("turn record missing digest block fields:\n%s", raw)
	}

	// Second run = cache hit (still exit 0; works even if the stack died).
	if code := Digest([]string{doc}); code != 0 {
		t.Fatalf("digest (cached) = %d, want 0", code)
	}
}

func TestDigest_UsageAndGuards(t *testing.T) {
	if code := Digest(nil); code != 2 {
		t.Errorf("digest (no arg) = %d, want 2", code)
	}

	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if code := Digest([]string{"whatever.md"}); code != 2 {
		t.Errorf("digest (no kb) = %d, want 2", code)
	}
}

func TestDigest_FabricatedCiteFailsHonestly(t *testing.T) {
	kb := kbTreeChdir(t)
	fakeStack(t, "A digest that fabricates D9999 as its foundation.\n"+strings.Repeat("Padding for the size floor. ", 10))
	doc := digestKB(t, kb)

	if code := Digest([]string{doc}); code != 1 {
		t.Fatalf("digest (fabricated cite) = %d, want 1", code)
	}
	home, _ := os.UserHomeDir()
	if entries, _ := os.ReadDir(filepath.Join(home, ".olifant", "digests")); len(entries) != 0 {
		t.Errorf("invalid digest reached the cache: %v", entries)
	}
}
