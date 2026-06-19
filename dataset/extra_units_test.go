package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapChars(t *testing.T) {
	if got := capChars("abcdef", 0); got != "abcdef" {
		t.Errorf("max<=0 should pass through, got %q", got)
	}
	if got := capChars("abc", 10); got != "abc" {
		t.Errorf("under cap = %q", got)
	}
	if got := capChars("abcdef", 3); got != "abc" {
		t.Errorf("over cap = %q, want abc", got)
	}
}

func TestItoa(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Errorf("itoa(0) = %q", got)
	}
	if got := itoa(9); got != "9" {
		t.Errorf("itoa(9) = %q", got)
	}
	if got := itoa(-1); got != "" {
		t.Errorf("itoa(-1) = %q, want empty", got)
	}
	if got := itoa(10); got != "" {
		t.Errorf("itoa(10) = %q, want empty", got)
	}
}

func TestSourceKindTier(t *testing.T) {
	tier1 := []SourceKind{SourceRetros, SourceDecisions, SourceAntipatterns, SourcePatterns, SourceFailureModes}
	for _, s := range tier1 {
		if s.Tier() != 1 {
			t.Errorf("Tier(%q) = %d, want 1", s, s.Tier())
		}
	}
	if SourceTriples.Tier() != 2 {
		t.Errorf("Tier(triples) = %d, want 2", SourceTriples.Tier())
	}
	if SourceKind("bogus").Tier() != 0 {
		t.Errorf("Tier(bogus) = %d, want 0", SourceKind("bogus").Tier())
	}
}

func TestCollectJSONL(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("tier1/a.jsonl")
	mk("tier1/b.jsonl")
	mk("tier2/c.jsonl")
	mk("tier1/notes.txt") // non-jsonl ignored

	// No subdir filter → all jsonl, sorted.
	all, err := collectJSONL(root, nil)
	if err != nil {
		t.Fatalf("collectJSONL: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("got %d files, want 3: %v", len(all), all)
	}
	if !sortedAscending(all) {
		t.Errorf("results not sorted: %v", all)
	}

	// Filter to tier1 only.
	t1, err := collectJSONL(root, []string{"tier1"})
	if err != nil {
		t.Fatalf("collectJSONL filtered: %v", err)
	}
	if len(t1) != 2 {
		t.Errorf("tier1 filter got %d, want 2: %v", len(t1), t1)
	}
	for _, p := range t1 {
		if !strings.Contains(filepath.ToSlash(p), "/tier1/") {
			t.Errorf("filtered result outside tier1: %q", p)
		}
	}
}

func sortedAscending(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

func TestDetectPatternHeadingLevel(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	h2 := write("h2.md", "# Title\n\n## A Section\n\nbody\n")
	if lvl, err := detectPatternHeadingLevel(h2); err != nil || lvl != 2 {
		t.Errorf("h2 doc = (%d,%v), want (2,nil)", lvl, err)
	}

	h3 := write("h3.md", "# Title\n\n### Only H3\n\nbody\n")
	if lvl, err := detectPatternHeadingLevel(h3); err != nil || lvl != 3 {
		t.Errorf("h3-only doc = (%d,%v), want (3,nil)", lvl, err)
	}

	fenced := write("fenced.md", "# Title\n\n```\n## not a heading\n```\n\ntext\n")
	if lvl, err := detectPatternHeadingLevel(fenced); err != nil || lvl != 3 {
		t.Errorf("fenced ## should not count = (%d,%v), want (3,nil)", lvl, err)
	}

	if _, err := detectPatternHeadingLevel(filepath.Join(dir, "missing.md")); err == nil {
		t.Error("missing file should error")
	}
}
