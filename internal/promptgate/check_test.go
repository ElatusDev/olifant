package promptgate

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDoc(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckDoc_CleanDocPasses(t *testing.T) {
	r := newFixtureResolver(t)
	doc := writeDoc(t, "# Design\n\nPer D210 and AP164 we do the thing.\nSee knowledge-base/decisions/log.md#D210 for detail.\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if !rep.Pass || rep.Unresolved != 0 {
		t.Errorf("clean doc: pass=%v unresolved=%d items=%+v", rep.Pass, rep.Unresolved, rep.Items)
	}
	if rep.Resolved != 3 { // D210, AP164, the kb path
		t.Errorf("resolved = %d, want 3 (%+v)", rep.Resolved, rep.Items)
	}
}

func TestCheckDoc_FabricatedCiteFailsWithLine(t *testing.T) {
	r := newFixtureResolver(t)
	doc := writeDoc(t, "line one D210\nline two cites D9999 which is fabricated\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if rep.Pass || rep.Unresolved != 1 {
		t.Fatalf("fabricated cite: pass=%v unresolved=%d", rep.Pass, rep.Unresolved)
	}
	var bad *CitedItem
	for i := range rep.Items {
		if rep.Items[i].Verdict == VerdictUnresolved {
			bad = &rep.Items[i]
		}
	}
	if bad == nil || bad.Cite != "D9999" || bad.Line != 2 {
		t.Errorf("unresolved item = %+v, want D9999 at line 2", bad)
	}
}

func TestCheckDoc_StaleDoesNotFail(t *testing.T) {
	r := newFixtureResolver(t)
	r.liveSHAs["decisions/log.md"] = "drifted-sha"
	doc := writeDoc(t, "Per D210 (defined in the decisions log).\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if !rep.Pass || rep.Stale != 1 || rep.Unresolved != 0 {
		t.Errorf("stale doc: pass=%v stale=%d unresolved=%d (D-OP7)", rep.Pass, rep.Stale, rep.Unresolved)
	}
}

func TestCheckDoc_UnknownPrefixPathsIgnored(t *testing.T) {
	r := newFixtureResolver(t)
	// olifant-internal paths are outside the validator's universe → ignored,
	// not failed; a known-prefix missing file IS judged.
	doc := writeDoc(t, "See internal/promptgate/resolver.go:55 and core-api/missing.md here.\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if rep.Unresolved != 1 || rep.Items[0].Cite != "core-api/missing.md" {
		t.Errorf("items = %+v, want only core-api/missing.md judged (unresolved)", rep.Items)
	}
}

func TestCheckDoc_LineSuffixAndPunctuationStripped(t *testing.T) {
	r := newFixtureResolver(t)
	doc := writeDoc(t, "Grounded in core-api/README.md:12-14, per the audit.\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if !rep.Pass || rep.Resolved != 1 || rep.Items[0].Cite != "core-api/README.md" {
		t.Errorf("line-suffixed path: %+v", rep.Items)
	}
}

func TestCheckDoc_DedupFirstLineWins(t *testing.T) {
	r := newFixtureResolver(t)
	doc := writeDoc(t, "D210 first\nD210 again\n")

	rep, err := r.CheckDoc(doc)
	if err != nil {
		t.Fatalf("CheckDoc: %v", err)
	}
	if len(rep.Items) != 1 || rep.Items[0].Line != 1 {
		t.Errorf("dedup: %+v, want single D210 at line 1", rep.Items)
	}
}

func TestCheckDoc_MissingFileErrors(t *testing.T) {
	r := newFixtureResolver(t)
	if _, err := r.CheckDoc(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Error("missing doc: want error")
	}
}
