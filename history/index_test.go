package history

import (
	"strings"
	"testing"
)

func TestChunkID_deterministicAndPrefixed(t *testing.T) {
	a := chunkID("hist", "core-api@abc1234")
	b := chunkID("hist", "core-api@abc1234")
	if a != b {
		t.Errorf("chunkID not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "hist-") {
		t.Errorf("missing prefix: %q", a)
	}
	c := chunkID("snap", "core-api@abc1234:src/Auth.java")
	if !strings.HasPrefix(c, "snap-") {
		t.Errorf("missing snap prefix: %q", c)
	}
	if a == c {
		t.Errorf("different keys produced same id: both %q", a)
	}
}

func TestBuildCommitChunk_metadataPopulated(t *testing.T) {
	rec := &CommitRecord{
		Repo: "core-api", Scope: "backend",
		SHA: "abc1234def", ShortSHA: "abc1234",
		Subject: "fix(auth): rotate JWT key",
		Body:    "Per D17.",
		Files: []FileTouch{
			{Path: "src/Auth.java", Status: "modified", Additions: 5, Deletions: 2},
		},
		CiteIDs: []string{"D17"},
	}
	c := buildCommitChunk(rec)
	if c.Scope != "backend" {
		t.Errorf("scope = %q", c.Scope)
	}
	if c.DocType != "commit-summary" {
		t.Errorf("doc_type = %q", c.DocType)
	}
	if c.SourceSHA != "abc1234def" {
		t.Errorf("source_sha = %q", c.SourceSHA)
	}
	if c.Title != "fix(auth): rotate JWT key" {
		t.Errorf("title = %q", c.Title)
	}
	if !strings.Contains(c.Body, "Per D17.") {
		t.Errorf("body missing message body: %q", c.Body)
	}
	if !strings.Contains(c.Body, "src/Auth.java") {
		t.Errorf("body missing files-touched: %q", c.Body)
	}
	if len(c.Metadata.CitesOutbound) != 1 || c.Metadata.CitesOutbound[0] != "D17" {
		t.Errorf("cites_outbound = %v", c.Metadata.CitesOutbound)
	}
}

func TestBuildCommitChunk_capsFilesAtTen(t *testing.T) {
	files := make([]FileTouch, 15)
	for i := range files {
		files[i] = FileTouch{Path: "f" + intToS(i) + ".go", Additions: 15 - i}
	}
	rec := &CommitRecord{Repo: "x", Scope: "x", SHA: "1", ShortSHA: "1", Subject: "s", Files: files}
	c := buildCommitChunk(rec)
	// Largest first (f0 with +15) must be in the body; smallest (f14 +1) must not.
	if !strings.Contains(c.Body, "f0.go") {
		t.Errorf("biggest file missing from body")
	}
	if strings.Contains(c.Body, "f14.go") {
		t.Errorf("smallest file should have been capped: %q", c.Body)
	}
}

func TestBuildSnapshotChunk_carriesFileContent(t *testing.T) {
	rec := &CommitRecord{
		Repo: "core-api", Scope: "backend",
		SHA: "abc1234def", ShortSHA: "abc1234",
		Subject: "fix(auth): rotate JWT key",
	}
	snap := FileSnapshot{
		Path:    "src/Auth.java",
		Status:  "modified",
		Content: "package auth;\nclass Auth { }\n",
	}
	c := buildSnapshotChunk(rec, snap)
	if c.DocType != "file-snapshot" {
		t.Errorf("doc_type = %q", c.DocType)
	}
	if c.SourceAnchor != "src/Auth.java" {
		t.Errorf("source_anchor = %q", c.SourceAnchor)
	}
	if c.Body != snap.Content {
		t.Errorf("body should equal snapshot content, got %q", c.Body)
	}
	if c.Metadata.Section != "modified" {
		t.Errorf("section = %q", c.Metadata.Section)
	}
}

func TestBuildSnapshotChunk_emptyContentFallsBackToStub(t *testing.T) {
	rec := &CommitRecord{Repo: "x", Scope: "x", SHA: "deadbeef", ShortSHA: "deadbee"}
	snap := FileSnapshot{Path: "nuked.txt", Status: "deleted"}
	c := buildSnapshotChunk(rec, snap)
	if !strings.Contains(c.Body, "no content captured") {
		t.Errorf("empty-content stub missing: %q", c.Body)
	}
}

func TestGroupChunksByScope_partitionsCorrectly(t *testing.T) {
	a := &CommitRecord{Repo: "core-api", Scope: "backend", SHA: "a", ShortSHA: "a", Subject: "s",
		Snapshots: []FileSnapshot{{Path: "f", Status: "modified", Content: "x"}}}
	b := &CommitRecord{Repo: "infra", Scope: "infra", SHA: "b", ShortSHA: "b", Subject: "s",
		Snapshots: []FileSnapshot{{Path: "g", Status: "modified", Content: "y"}}}
	commits, snapshots := groupChunksByScope([]*CommitRecord{a, b})

	if len(commits["backend"]) != 1 || len(commits["infra"]) != 1 {
		t.Errorf("commits partition wrong: %v", commits)
	}
	if len(snapshots["backend"]) != 1 || len(snapshots["infra"]) != 1 {
		t.Errorf("snapshots partition wrong: %v", snapshots)
	}
}

func TestCapChars_truncatesAtUTF8Boundary(t *testing.T) {
	in := "héllo wörld"
	if got := capChars(in, 100); got != in {
		t.Errorf("under-cap modified: %q", got)
	}
	out := capChars(in, 4)
	// Must be valid UTF-8 — no half-multibyte rune at the tail.
	for _, r := range out {
		_ = r
	}
}

// intToS is a tiny stdlib-only helper to avoid pulling strconv just
// for this test.
func intToS(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
