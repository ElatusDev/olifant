package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===== cites.go =====

func TestExtractCites(t *testing.T) {
	if got := ExtractCites(""); got != nil {
		t.Errorf("empty body = %v, want nil", got)
	}
	if got := ExtractCites("no identifiers here"); got != nil {
		t.Errorf("no-cite body = %v, want nil", got)
	}
	body := "See D154 and AP78, plus ABS-04 and SB-15. Again D154."
	got := ExtractCites(body)
	want := []string{"ABS-04", "AP78", "D154", "SB-15"} // deduped + sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ExtractCites = %v, want %v", got, want)
	}
}

func TestExtractCites_WebappAndTesting(t *testing.T) {
	got := ExtractCites("rules WA-L, AWC-12, TBU-01, OL-3, SS-2")
	want := []string{"AWC-12", "OL-3", "SS-2", "TBU-01", "WA-L"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ExtractCites = %v, want %v", got, want)
	}
}

// ===== scope.go =====

func TestScopeForKBPath(t *testing.T) {
	cases := map[string]string{
		"retrospectives/akademia-plus-go/x.md": ScopeMobile,
		"workflows/core-api-e2e/y.md":          ScopeE2E,
		"prompts/infra/z.md":                   ScopeInfra,
		"skills/foo.md":                        ScopePlatformProcess,
		"standards/CODE-QUALITY.yaml":          ScopeUniversal,
		"decisions/log.yaml":                   ScopeUniversal,
		"unmapped/path.md":                     "",
	}
	for path, want := range cases {
		if got := ScopeForKBPath(path); got != want {
			t.Errorf("ScopeForKBPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestScopeForRepoClaudeMd(t *testing.T) {
	cases := map[string]string{
		"core-api":              ScopeBackend,
		"elatusdev-web":         ScopeWebapp,
		"akademia-plus-central": ScopeMobile,
		"akademia-plus-go":      ScopeMobile,
		"core-api-e2e":          ScopeE2E,
		"infra":                 ScopeInfra,
		"unknown-repo":          "",
	}
	for repo, want := range cases {
		if got := ScopeForRepoClaudeMd(repo); got != want {
			t.Errorf("ScopeForRepoClaudeMd(%q) = %q, want %q", repo, got, want)
		}
	}
}

func TestDocTypeForPath(t *testing.T) {
	cases := []struct{ path, ext, want string }{
		{"standards/x.yaml", ".yaml", "standard"},
		{"decisions/log.yaml", ".yaml", "decision"},
		{"anti-patterns/catalog.yaml", ".yaml", "anti_pattern"},
		{"patterns/backend.md", ".md", "pattern"},
		{"retrospectives/core-api/r.md", ".md", "retro"},
		{"workflows/x.md", ".md", "workflow"},
		{"prompts/x.md", ".md", "prompt"},
		{"templates/x.md", ".md", "template"},
		{"skills/x.md", ".md", "skill"},
		{"architecture/x.md", ".md", "architecture"},
		{"ux/x.md", ".md", "view"},
		{"execution-reports/x.md", ".md", "execution_report"},
		{"dsl/x.md", ".md", "meta"},
		{"standards/x.md", ".md", "doc"}, // standards but not .yaml → default
		{"misc/readme.md", ".md", "doc"},
	}
	for _, c := range cases {
		if got := docTypeForPath(c.path, c.ext); got != c.want {
			t.Errorf("docTypeForPath(%q,%q) = %q, want %q", c.path, c.ext, got, c.want)
		}
	}
}

// ===== builder.go helpers =====

func TestIsStructuredYAML(t *testing.T) {
	yes := []string{"standards/CODE.yaml", "decisions/log.yaml", "anti-patterns/catalog.yaml", "dictionary/universal/domain.yaml"}
	for _, p := range yes {
		if !isStructuredYAML(p) {
			t.Errorf("isStructuredYAML(%q) = false, want true", p)
		}
	}
	no := []string{"standards/CODE.md", "decisions/other.yaml", "patterns/backend.yaml", "random.yaml"}
	for _, p := range no {
		if isStructuredYAML(p) {
			t.Errorf("isStructuredYAML(%q) = true, want false", p)
		}
	}
}

func TestIsIndexableExt(t *testing.T) {
	if !isIndexableExt(".md") || !isIndexableExt(".yaml") {
		t.Error(".md/.yaml should be indexable")
	}
	if isIndexableExt(".go") || isIndexableExt(".json") {
		t.Error(".go/.json should not be indexable")
	}
}

func TestShouldSkipDir(t *testing.T) {
	// short-term = ledger + eval-run model output; indexing it puts model
	// output into retrievable truth (D-BK9 breach found by the R6 spike).
	for _, d := range []string{".git", "node_modules", "target", "dist", "build", ".idea", ".vscode", "v1", "short-term"} {
		if !shouldSkipDir(d) {
			t.Errorf("shouldSkipDir(%q) = false, want true", d)
		}
	}
	if shouldSkipDir("standards") {
		t.Error("shouldSkipDir(standards) = true, want false")
	}
}

func TestAppendUnique(t *testing.T) {
	s := appendUnique(nil, "a")
	s = appendUnique(s, "b")
	s = appendUnique(s, "a") // dup — ignored
	s = appendUnique(s, "")  // empty — ignored
	if strings.Join(s, ",") != "a,b" {
		t.Errorf("appendUnique result = %v, want [a b]", s)
	}
}

func TestWriteNDJSON_And_WriteManifest(t *testing.T) {
	dir := t.TempDir()
	nd := filepath.Join(dir, "out.ndjson")
	if err := writeNDJSON(nd, []Chunk{{ChunkID: "1", Body: "a <b>"}, {ChunkID: "2", Body: "c"}}); err != nil {
		t.Fatalf("writeNDJSON: %v", err)
	}
	raw, _ := os.ReadFile(nd)
	if !strings.Contains(string(raw), "<b>") {
		t.Errorf("HTML escaped (SetEscapeHTML(false) not honored): %s", raw)
	}
	if n := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; n != 2 {
		t.Errorf("wrote %d lines, want 2", n)
	}

	mf := filepath.Join(dir, "manifest.yaml")
	if err := writeManifest(mf, Manifest{BuilderVersion: BuilderVersion, TotalChunks: 2}); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	mraw, _ := os.ReadFile(mf)
	if !strings.Contains(string(mraw), "total_chunks: 2") {
		t.Errorf("manifest body unexpected:\n%s", mraw)
	}
}

// ===== indexer.go helpers =====

func TestCapInput(t *testing.T) {
	if got := capInput("short", 100); got != "short" {
		t.Errorf("under cap = %q", got)
	}
	if got := capInput("abcdef", 3); got != "abc" {
		t.Errorf("ascii cap = %q, want abc", got)
	}
	if got := capInput("aé", 2); got != "a" { // é is 2 bytes; must back off the partial rune
		t.Errorf("multibyte cap = %q, want a", got)
	}
}

func TestChunkMetadataForChroma(t *testing.T) {
	c := Chunk{
		Source: "decisions/log.yaml", Scope: "universal", DocType: "decision",
		SourceSHA: "sha", SourceAnchor: "decisions/log.yaml#D154", ArtifactID: "D154",
		Title: "Cloud synth", Metadata: ChunkMetadata{
			Section: "sec", Severity: "BLOCKER", Status: "ACCEPTED",
			CitesOutbound: []string{"D1", "D2"}, CitesInbound: []string{"AP9"},
			TechTags: []string{"java"},
		},
	}
	m := chunkMetadataForChroma(c)
	if m["artifact_id"] != "D154" || m["severity"] != "BLOCKER" {
		t.Errorf("scalar fields wrong: %v", m)
	}
	if m["cites_outbound"] != "D1,D2" || m["cites_inbound"] != "AP9" || m["tech_tags"] != "java" {
		t.Errorf("joined fields wrong: %v", m)
	}

	bare := chunkMetadataForChroma(Chunk{Source: "x", Scope: "y", DocType: "doc"})
	for _, k := range []string{"artifact_id", "title", "severity", "cites_outbound"} {
		if _, ok := bare[k]; ok {
			t.Errorf("empty field %q should be omitted", k)
		}
	}
}

func TestReadChunks(t *testing.T) {
	dir := t.TempDir()
	if got, err := readChunks(filepath.Join(dir, "missing.ndjson")); err != nil || got != nil {
		t.Errorf("missing file = (%v,%v), want (nil,nil)", got, err)
	}

	good := filepath.Join(dir, "good.ndjson")
	_ = os.WriteFile(good, []byte(`{"chunk_id":"1","source":"a","scope":"backend","doc_type":"code","body":"x"}`+"\n"), 0o644)
	chunks, err := readChunks(good)
	if err != nil || len(chunks) != 1 || chunks[0].ChunkID != "1" {
		t.Errorf("readChunks good = (%v,%v)", chunks, err)
	}

	bad := filepath.Join(dir, "bad.ndjson")
	_ = os.WriteFile(bad, []byte("{not json}\n"), 0o644)
	if _, err := readChunks(bad); err == nil {
		t.Error("invalid json should error")
	}
}

func TestDiscoverScopes(t *testing.T) {
	if got, _ := discoverScopes("/ignored", []string{"backend", "infra"}); strings.Join(got, ",") != "backend,infra" {
		t.Errorf("only-filter = %v", got)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "backend.ndjson"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "infra.ndjson"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "manifest.yaml"), nil, 0o644)
	got, err := discoverScopes(dir, nil)
	if err != nil {
		t.Fatalf("discoverScopes: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("discovered %v, want 2 ndjson scopes", got)
	}

	if _, err := discoverScopes(filepath.Join(dir, "nope"), nil); err == nil {
		t.Error("missing corpus dir should error")
	}
}

// ===== types.go =====

func TestNowISO(t *testing.T) {
	if _, err := time.Parse(time.RFC3339, nowISO()); err != nil {
		t.Errorf("nowISO not RFC3339: %v", err)
	}
}

// ===== yaml_chunker.go =====

func TestMakeChunkID(t *testing.T) {
	a := makeChunkID("decisions/log.yaml", "anchor", "body")
	if a != makeChunkID("decisions/log.yaml", "anchor", "body") {
		t.Error("not deterministic")
	}
	if a == makeChunkID("decisions/log.yaml", "anchor", "BODY") {
		t.Error("body change ignored")
	}
	if a == makeChunkID("other/path.yaml", "anchor", "body") {
		t.Error("source change ignored")
	}
	if len(a) != 40 {
		t.Errorf("sha1 hex length = %d", len(a))
	}
}

func TestChunkYAML_SequenceForm(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.yaml")
	_ = os.WriteFile(p, []byte(`- id: D1
  title: First
  severity: blocker
  status: accepted
- note: no identifier key here
  body_text: a thing
`), 0o644)

	chunks, err := chunkYAML(p, "decisions/log.yaml", "universal", "decision", "sha")
	if err != nil {
		t.Fatalf("chunkYAML: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].ArtifactID != "D1" || chunks[0].Title != "First" {
		t.Errorf("chunk0 = %+v", chunks[0])
	}
	if chunks[0].Metadata.Severity != "BLOCKER" || chunks[0].Metadata.Status != "ACCEPTED" {
		t.Errorf("severity/status not uppercased: %+v", chunks[0].Metadata)
	}
	if chunks[0].SourceAnchor != "decisions/log.yaml#D1" {
		t.Errorf("anchor = %q", chunks[0].SourceAnchor)
	}
	// second entry has no id key → item-indexed anchor
	if chunks[1].SourceAnchor != "decisions/log.yaml#item-1" {
		t.Errorf("no-id anchor = %q", chunks[1].SourceAnchor)
	}
}

func TestChunkYAML_MappingOfSequences(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.yaml")
	_ = os.WriteFile(p, []byte(`backend:
  - id: ABS-04
    title: No field injection
webapp:
  - id: AWC-01
    title: Foo
`), 0o644)
	chunks, err := chunkYAML(p, "anti-patterns/catalog.yaml", "universal", "anti_pattern", "sha")
	if err != nil {
		t.Fatalf("chunkYAML: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	ids := chunks[0].ArtifactID + "," + chunks[1].ArtifactID
	if !strings.Contains(ids, "ABS-04") || !strings.Contains(ids, "AWC-01") {
		t.Errorf("artifact ids = %q", ids)
	}
}

func TestChunkYAML_EmptyAndInvalid(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.yaml")
	_ = os.WriteFile(empty, []byte(""), 0o644)
	if chunks, err := chunkYAML(empty, "x.yaml", "u", "d", "s"); err != nil || chunks != nil {
		t.Errorf("empty yaml = (%v,%v), want (nil,nil)", chunks, err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("key: [unterminated"), 0o644)
	if _, err := chunkYAML(bad, "x.yaml", "u", "d", "s"); err == nil {
		t.Error("invalid yaml should error")
	}

	if _, err := chunkYAML(filepath.Join(dir, "missing.yaml"), "x.yaml", "u", "d", "s"); err == nil {
		t.Error("missing file should error")
	}
}

func TestNodeToInterface_RoundTrip(t *testing.T) {
	// Exercised indirectly via chunkYAML, but assert scalar/seq/map handling here.
	p := filepath.Join(t.TempDir(), "x.yaml")
	_ = os.WriteFile(p, []byte(`- id: D1
  tags: [a, b]
  nested:
    k: v
`), 0o644)
	chunks, err := chunkYAML(p, "x.yaml", "u", "d", "s")
	if err != nil || len(chunks) != 1 {
		t.Fatalf("chunkYAML = (%v,%v)", chunks, err)
	}
	body := chunks[0].Body
	for _, want := range []string{"tags:", "- a", "nested:", "k: v"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// ===== md_chunker.go =====

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Goals and Determinism": "goals-and-determinism",
		"Foo_Bar-Baz":           "foo-bar-baz",
	}
	for in, want := range cases {
		if got := slugify(in, 0); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugify("", 5); got != "section-5" {
		t.Errorf("empty heading = %q, want section-5", got)
	}
	if got := slugify("@@@!!!", 3); got != "section-3" {
		t.Errorf("all-stripped = %q, want section-3", got)
	}
	if got := slugify(strings.Repeat("a", 100), 0); len(got) != 80 {
		t.Errorf("runaway slug length = %d, want 80", len(got))
	}
}

func TestIfNonEmpty(t *testing.T) {
	if got := ifNonEmpty(" > Title", "Title"); got != " > Title" {
		t.Errorf("non-empty = %q", got)
	}
	if got := ifNonEmpty(" > Title", ""); got != "" {
		t.Errorf("empty raw = %q, want empty", got)
	}
}

func TestSplitSectionsByHeading(t *testing.T) {
	md := "# Doc Title\n\nintro\n\n## Alpha\n\nbody a\n\n## Beta\n\nbody b\n"
	title, secs := splitSectionsByHeading(md, 2)
	if title != "Doc Title" {
		t.Errorf("doc title = %q", title)
	}
	if len(secs) != 2 || secs[0].Title != "Alpha" || secs[1].Title != "Beta" {
		t.Fatalf("sections = %+v", secs)
	}
}

func TestSplitSectionsByHeading_FenceNotSplit(t *testing.T) {
	md := "## Real\n\n```\n## not-a-heading inside fence\n```\nafter\n"
	_, secs := splitSectionsByHeading(md, 2)
	if len(secs) != 1 {
		t.Fatalf("fence content split into %d sections, want 1", len(secs))
	}
	if !strings.Contains(secs[0].Body, "## not-a-heading") {
		t.Errorf("fenced heading lost: %q", secs[0].Body)
	}
}

func TestMergeSlivers(t *testing.T) {
	if got := mergeSlivers([]mdSection{{Title: "solo", Body: "x"}}, 200); len(got) != 1 {
		t.Errorf("single section should pass through, got %d", len(got))
	}
	secs := []mdSection{
		{Title: "A", Heading: "A", Body: "short"},                  // sliver < 200
		{Title: "B", Heading: "B", Body: strings.Repeat("x", 300)}, // big
		{Title: "C", Heading: "C", Body: strings.Repeat("y", 300)}, // big
	}
	got := mergeSlivers(secs, 200)
	if len(got) != 2 {
		t.Fatalf("merged into %d, want 2 (A merges forward into B)", len(got))
	}
	if !strings.Contains(got[0].Title, "A + B") {
		t.Errorf("merged title = %q, want 'A + B'", got[0].Title)
	}
}

func TestSplitByParagraph(t *testing.T) {
	// Two paragraphs that together exceed maxChars → two chunks.
	body := strings.Repeat("a", 120) + "\n\n" + strings.Repeat("b", 120) + "\n"
	parts := splitByParagraph(body, 150)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if !strings.HasPrefix(parts[0], "a") || !strings.HasPrefix(parts[1], "b") {
		t.Errorf("paragraph packing wrong: %q / %q", parts[0][:1], parts[1][:1])
	}
}

func TestChunkMarkdown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "doc.md")
	body := "# My Doc\n\n## Section One\n\nThis cites D154 and AP78.\n\n## Section Two\n\nMore text here that is reasonably long " +
		strings.Repeat("word ", 60) + "\n"
	_ = os.WriteFile(p, []byte(body), 0o644)

	chunks, err := chunkMarkdown(p, "patterns/backend.md", "backend", "pattern", "sha1")
	if err != nil {
		t.Fatalf("chunkMarkdown: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	first := chunks[0]
	if !strings.HasPrefix(first.Body, "[pattern] My Doc") {
		t.Errorf("breadcrumb header missing:\n%s", first.Body)
	}
	if first.Scope != "backend" || first.SourceSHA != "sha1" {
		t.Errorf("chunk meta = scope:%q sha:%q", first.Scope, first.SourceSHA)
	}
	if !strings.HasPrefix(first.SourceAnchor, "patterns/backend.md#") {
		t.Errorf("anchor = %q", first.SourceAnchor)
	}
	// Cites extracted from the section that mentions them.
	var sawCites bool
	for _, c := range chunks {
		if len(c.Metadata.CitesOutbound) > 0 {
			sawCites = true
		}
	}
	if !sawCites {
		t.Error("expected at least one chunk to carry outbound cites (D154/AP78)")
	}
}

func TestChunkMarkdown_NoH2SingleChunk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "flat.md")
	_ = os.WriteFile(p, []byte("# Title\n\njust a flat doc with no sections\n"), 0o644)
	chunks, err := chunkMarkdown(p, "architecture/x.md", "universal", "architecture", "s")
	if err != nil {
		t.Fatalf("chunkMarkdown: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("flat doc → %d chunks, want 1", len(chunks))
	}
}

func TestChunkMarkdown_MissingFileErrors(t *testing.T) {
	if _, err := chunkMarkdown(filepath.Join(t.TempDir(), "nope.md"), "x.md", "u", "doc", "s"); err == nil {
		t.Error("missing file should error")
	}
}

// ===== builder.go chunkOne dispatch =====

func TestChunkOne_Dispatch(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "p.md")
	_ = os.WriteFile(mdPath, []byte("# T\n\n## S\n\nbody\n"), 0o644)
	if chunks, err := chunkOne(mdPath, "patterns/p.md", "backend", "pattern", ".md", "s"); err != nil || len(chunks) == 0 {
		t.Errorf("md dispatch = (%v,%v)", chunks, err)
	}

	yamlPath := filepath.Join(dir, "log.yaml")
	_ = os.WriteFile(yamlPath, []byte("- id: D1\n  title: T\n"), 0o644)
	if chunks, err := chunkOne(yamlPath, "decisions/log.yaml", "universal", "decision", ".yaml", "s"); err != nil || len(chunks) != 1 {
		t.Errorf("structured-yaml dispatch = (%v,%v)", chunks, err)
	}

	// Non-structured yaml + unknown ext → nil, nil.
	if chunks, err := chunkOne(yamlPath, "configs/other.yaml", "u", "d", ".yaml", "s"); err != nil || chunks != nil {
		t.Errorf("non-structured yaml = (%v,%v), want (nil,nil)", chunks, err)
	}
	if chunks, err := chunkOne("x.go", "x.go", "u", "d", ".go", "s"); err != nil || chunks != nil {
		t.Errorf("unknown ext = (%v,%v), want (nil,nil)", chunks, err)
	}
}
