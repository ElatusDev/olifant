package dictionary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCategorizeArtifactID(t *testing.T) {
	cases := []struct {
		id, docType, want string
	}{
		{"D154", "", "domain.decision"},
		{"AP78", "", "domain.anti_pattern"},
		{"ABS-04", "", "domain.anti_pattern.backend"},
		{"AWC-12", "", "domain.anti_pattern.webapp"},
		{"AMS-03", "", "domain.anti_pattern.mobile"},
		{"SB-15", "", "domain.standard_rule.security"},
		{"WA-L", "", "domain.standard_rule.architecture.webapp"},
		{"WA-F-1", "", "domain.standard_rule.architecture.webapp"},
		{"TBU-01", "", "domain.standard_rule.testing.backend"},
		{"TWC-02", "", "domain.standard_rule.testing.webapp"},
		{"TMU-09", "", "domain.standard_rule.testing.mobile"},
		{"OL-3", "", "domain.standard_rule.observability"},
		{"U-7", "", "domain.standard_rule.quality"},
		{"SS-2", "", "domain.standard_rule.schema"},
		{"RL1", "", "domain.retrospective_rule"},
		{"nonsense", "", ""},
		{"D", "", ""},      // missing digits
		{"ABS-", "", ""},   // missing digits
		{"abs-04", "", ""}, // case-sensitive
	}
	for _, c := range cases {
		if got := categorizeArtifactID(c.id, c.docType); got != c.want {
			t.Errorf("categorizeArtifactID(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestScopeForCategory(t *testing.T) {
	cases := []struct{ category, chunkScope, want string }{
		{"domain.anti_pattern.backend", "ignored", ScopeBackend},
		{"domain.standard_rule.testing.webapp", "", ScopeWebapp},
		{"domain.anti_pattern.mobile", "", ScopeMobile},
		{"domain.decision", "backend", ScopeUniversal},
		{"domain.standard_rule.security", "", ScopeUniversal},
	}
	for _, c := range cases {
		if got := scopeForCategory(c.category, c.chunkScope); got != c.want {
			t.Errorf("scopeForCategory(%q,%q) = %q, want %q", c.category, c.chunkScope, got, c.want)
		}
	}
}

func TestBestDefinition(t *testing.T) {
	cases := []struct{ name, title, body, want string }{
		{"title wins", "  Tenant stamping  ", "anything", "Tenant stamping"},
		{"first sentence from body", "", "This is the rule. And more.", "This is the rule"},
		{"strips breadcrumb line", "", "[backend > rules]\nActual definition here.", "Actual definition here"},
		{"newline terminates", "", "line one\nline two", "line one"},
		{"no terminator returns whole body", "", "no terminator here", "no terminator here"},
	}
	for _, c := range cases {
		if got := bestDefinition(c.title, c.body); got != c.want {
			t.Errorf("%s: bestDefinition(%q,%q) = %q, want %q", c.name, c.title, c.body, got, c.want)
		}
	}
}

func TestBestDefinition_Truncates(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := bestDefinition("", long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long definition not ellipsised: ...%q", got[len(got)-5:])
	}
	if want := 240 + len("…"); len(got) != want {
		t.Errorf("truncated length = %d, want %d", len(got), want)
	}
}

func TestReadDomain_MissingFileIsEmpty(t *testing.T) {
	entries, err := readDomain(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should be (nil,nil), got err %v", err)
	}
	if entries != nil {
		t.Errorf("want nil entries, got %v", entries)
	}
}

func TestReadDomain_InvalidYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(p, []byte("\tnot: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDomain(p); err == nil {
		t.Error("invalid yaml should error")
	}
}

func TestWriteThenReadDomain_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "domain.yaml")
	in := []Entry{{Term: "D1", Category: "domain.decision", Definition: "x", Cites: []string{"log.md"}}}
	if err := writeDomain(p, in); err != nil {
		t.Fatalf("writeDomain: %v", err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "# Olifant CNL") {
		t.Errorf("missing header:\n%s", raw)
	}
	out, err := readDomain(p)
	if err != nil {
		t.Fatalf("readDomain: %v", err)
	}
	if len(out) != 1 || out[0].Term != "D1" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

// writeNDJSON writes the given JSON lines to <dir>/<name>.
func writeNDJSON(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrap_EndToEnd(t *testing.T) {
	root := t.TempDir()
	corpusDir := filepath.Join(root, "corpus")
	dictDir := filepath.Join(root, "dictionary")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeNDJSON(t, corpusDir, "universal.ndjson",
		`{"artifact_id":"D154","title":"Cloud synth promote","scope":"universal","source_anchor":"log.md#d154"}`,
		`{"artifact_id":"D154","title":"dup","scope":"universal"}`, // duplicate term — skipped
		`{"artifact_id":"","title":"no id"}`,                       // empty artifact_id — skipped
		`{"artifact_id":"nonsense-id","title":"unrecognized"}`,     // category "" — skipped
		`{not valid json`, // unmarshal error — skipped
	)
	writeNDJSON(t, corpusDir, "backend.ndjson",
		`{"artifact_id":"ABS-04","title":"No field injection","scope":"backend","source":"anti-patterns/catalog.md"}`,
	)
	// a non-ndjson file in the dir should be ignored
	if err := os.WriteFile(filepath.Join(corpusDir, "manifest.yaml"), []byte("x: 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := Bootstrap(BootstrapConfig{CorpusDir: corpusDir, DictionaryDir: dictDir})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if stats.UniqueArtifactID != 2 {
		t.Errorf("UniqueArtifactID = %d, want 2 (D154, ABS-04)", stats.UniqueArtifactID)
	}
	if stats.EntriesAdded != 2 {
		t.Errorf("EntriesAdded = %d, want 2", stats.EntriesAdded)
	}
	if stats.PerScopeAdded[ScopeUniversal] != 1 || stats.PerScopeAdded[ScopeBackend] != 1 {
		t.Errorf("PerScopeAdded = %v, want universal:1 backend:1", stats.PerScopeAdded)
	}

	// D154 landed in universal with the anchor cite.
	uni, err := readDomain(filepath.Join(dictDir, ScopeUniversal, "domain.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(uni) != 1 || uni[0].Term != "D154" {
		t.Fatalf("universal domain = %+v", uni)
	}
	if uni[0].Cites[0] != "log.md#d154" {
		t.Errorf("cite = %v, want source_anchor", uni[0].Cites)
	}
	if uni[0].IntroducedBy != "corpus-bootstrap" {
		t.Errorf("IntroducedBy = %q", uni[0].IntroducedBy)
	}

	// ABS-04 landed in backend, citing source (no anchor present).
	be, _ := readDomain(filepath.Join(dictDir, ScopeBackend, "domain.yaml"))
	if len(be) != 1 || be[0].Cites[0] != "anti-patterns/catalog.md" {
		t.Errorf("backend domain = %+v", be)
	}
}

func TestBootstrap_SkipsExistingTermsOnRerun(t *testing.T) {
	root := t.TempDir()
	corpusDir := filepath.Join(root, "corpus")
	dictDir := filepath.Join(root, "dictionary")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNDJSON(t, corpusDir, "universal.ndjson",
		`{"artifact_id":"D154","title":"Decision","scope":"universal"}`,
	)

	if _, err := Bootstrap(BootstrapConfig{CorpusDir: corpusDir, DictionaryDir: dictDir}); err != nil {
		t.Fatal(err)
	}
	// Second run: term already present → skipped, nothing added.
	stats, err := Bootstrap(BootstrapConfig{CorpusDir: corpusDir, DictionaryDir: dictDir, Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.EntriesAdded != 0 || stats.EntriesSkipped != 1 {
		t.Errorf("rerun stats: added=%d skipped=%d, want 0/1", stats.EntriesAdded, stats.EntriesSkipped)
	}
}

func TestBootstrap_DryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	corpusDir := filepath.Join(root, "corpus")
	dictDir := filepath.Join(root, "dictionary")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNDJSON(t, corpusDir, "universal.ndjson",
		`{"artifact_id":"D154","title":"Decision","scope":"universal"}`,
	)
	stats, err := Bootstrap(BootstrapConfig{CorpusDir: corpusDir, DictionaryDir: dictDir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.EntriesAdded != 1 {
		t.Errorf("dry-run should still count EntriesAdded=1, got %d", stats.EntriesAdded)
	}
	if _, err := os.Stat(filepath.Join(dictDir, ScopeUniversal, "domain.yaml")); !os.IsNotExist(err) {
		t.Error("dry-run must not write the domain file")
	}
}

func TestBootstrap_MissingCorpusDirErrors(t *testing.T) {
	_, err := Bootstrap(BootstrapConfig{
		CorpusDir:     filepath.Join(t.TempDir(), "does-not-exist"),
		DictionaryDir: t.TempDir(),
	})
	if err == nil {
		t.Error("missing corpus dir should error")
	}
}
