package history

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSplitMessage_subjectOnly(t *testing.T) {
	subject, body := splitMessage("fix(auth): rotate JWT signing key")
	if subject != "fix(auth): rotate JWT signing key" {
		t.Errorf("subject = %q", subject)
	}
	if body != "" {
		t.Errorf("body = %q", body)
	}
}

func TestSplitMessage_subjectAndBody(t *testing.T) {
	msg := "fix(auth): rotate JWT signing key\n\nKey was leaked in PR #41.\nRotated per D17.\n"
	subject, body := splitMessage(msg)
	if subject != "fix(auth): rotate JWT signing key" {
		t.Errorf("subject = %q", subject)
	}
	if body != "Key was leaked in PR #41.\nRotated per D17." {
		t.Errorf("body = %q", body)
	}
}

func TestStripAttribution_coAuthoredByClaude(t *testing.T) {
	body := "Body line 1\nBody line 2\n\nCo-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>\n\nMore body"
	got := stripAttribution(body)
	if strings.Contains(got, "Claude") {
		t.Errorf("stripped body still contains 'Claude': %q", got)
	}
	if strings.Contains(got, "anthropic") {
		t.Errorf("stripped body still contains 'anthropic': %q", got)
	}
	if !strings.Contains(got, "Body line 1") || !strings.Contains(got, "More body") {
		t.Errorf("real prose was lost: %q", got)
	}
}

func TestStripAttribution_robotEmoji(t *testing.T) {
	body := "Body line\n\n🤖 Generated with Claude Code\n\nFooter"
	got := stripAttribution(body)
	if strings.Contains(got, "🤖") {
		t.Errorf("emoji line not stripped: %q", got)
	}
	if !strings.Contains(got, "Footer") {
		t.Errorf("real prose was lost: %q", got)
	}
}

func TestStripAttribution_collapsesBlanks(t *testing.T) {
	body := "Line A\n\n\n\n\nLine B"
	got := stripAttribution(body)
	if got != "Line A\n\nLine B" {
		t.Errorf("collapse failed: got %q", got)
	}
}

func TestStripAttribution_preservesLegitimateCoAuthor(t *testing.T) {
	body := "Body\n\nCo-authored-by: David Martinez <david@example.com>\n\nFooter"
	got := stripAttribution(body)
	if !strings.Contains(got, "David Martinez") {
		t.Errorf("legitimate co-author dropped: %q", got)
	}
}

func TestExtractCites_findsAllDialects(t *testing.T) {
	msg := "Closes D17 and D122. Avoids AP14, AP85. Per SB-04, WA-L13, TBU-22, AMS-06, ABS-15, RK-06b. References CI1, PC3."
	got := extractCites(msg)
	want := []string{"D17", "D122", "AP14", "AP85", "SB-04", "WA-L13", "TBU-22", "AMS-06", "ABS-15", "RK-06b", "CI1", "PC3"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractCites diff:\n got  %v\n want %v", got, want)
	}
}

func TestExtractCites_dedupes(t *testing.T) {
	msg := "D17 again D17 then AP14 and AP14 once more"
	got := extractCites(msg)
	want := []string{"D17", "AP14"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCapBytes_underCapUnchanged(t *testing.T) {
	in := "small content"
	got := capBytes(in, 100)
	if got != in {
		t.Errorf("under-cap was modified: %q", got)
	}
}

func TestCapBytes_overCapTruncatedWithMarker(t *testing.T) {
	in := strings.Repeat("line1\n", 100) // 600 bytes
	got := capBytes(in, 60)
	if !strings.Contains(got, "[truncated, original 600 bytes]") {
		t.Errorf("missing truncation marker: %q", got)
	}
	if len(got) > 200 {
		t.Errorf("truncated output too large: %d bytes", len(got))
	}
}

func TestCapBytes_truncatesAtNewlineBoundary(t *testing.T) {
	in := "aaaa\nbbbb\ncccc\ndddd\n"
	got := capBytes(in, 13)
	// Should land at end of "bbbb\n" (10 bytes) not mid-line.
	if !strings.HasPrefix(got, "aaaa\nbbbb") {
		t.Errorf("did not snap to newline: %q", got)
	}
}

func TestSplitUnifiedDiff_perFileSections(t *testing.T) {
	patch := `diff --git a/foo.go b/foo.go
index 1234..5678 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
 context
-old
+new
diff --git a/bar.go b/bar.go
new file mode 100644
--- /dev/null
+++ b/bar.go
@@ -0,0 +1,2 @@
+package bar
+
`
	parts := splitUnifiedDiff(patch)
	if len(parts) != 2 {
		t.Fatalf("expected 2 sections, got %d:\n%v", len(parts), parts)
	}
	if !strings.HasPrefix(parts[0], "diff --git a/foo.go") {
		t.Errorf("part 0 wrong prefix: %q", parts[0][:40])
	}
	if !strings.HasPrefix(parts[1], "diff --git a/bar.go") {
		t.Errorf("part 1 wrong prefix: %q", parts[1][:40])
	}
	if strings.Contains(parts[0], "package bar") {
		t.Errorf("part 0 leaked into part 1's content")
	}
}

func TestSplitUnifiedDiff_empty(t *testing.T) {
	if got := splitUnifiedDiff(""); got != nil {
		t.Errorf("empty input should be nil, got %v", got)
	}
}

func TestCountLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
	}
	for _, c := range cases {
		if got := countLines(c.in); got != c.want {
			t.Errorf("countLines(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuildCommitSummary_shape(t *testing.T) {
	rec := &CommitRecord{
		Repo:     "core-api",
		Scope:    "backend",
		SHA:      "abc1234def5678",
		ShortSHA: "abc1234",
		Author:   "Test <t@example.com>",
		Subject:  "fix(auth): rotate JWT key",
		Body:     "Per D17.",
		Files: []FileTouch{
			{Path: "src/auth.java", Status: "modified", Additions: 5, Deletions: 2},
			{Path: "src/util.java", Status: "modified", Additions: 1, Deletions: 0},
		},
		CiteIDs: []string{"D17"},
	}
	ex := BuildCommitSummary(rec, 10)

	if ex.Family != "commit-summary" {
		t.Errorf("family = %q", ex.Family)
	}
	if ex.Role != "validate" {
		t.Errorf("role = %q", ex.Role)
	}
	if ex.Source != "core-api@abc1234" {
		t.Errorf("source = %q", ex.Source)
	}
	if got := ex.Metadata["cite_ids"]; got != "D17" {
		t.Errorf("cite_ids = %q", got)
	}
	assistant := ex.Messages[1].Content
	if !strings.Contains(assistant, "fix(auth): rotate JWT key") {
		t.Errorf("subject missing: %q", assistant)
	}
	if !strings.Contains(assistant, "src/auth.java (modified, +5/-2)") {
		t.Errorf("file row missing: %q", assistant)
	}
}

func TestBuildCommitSummary_filesListCapOverflow(t *testing.T) {
	files := make([]FileTouch, 0, 15)
	for i := 0; i < 15; i++ {
		files = append(files, FileTouch{
			Path:      "f" + intToStr(i) + ".go",
			Status:    "modified",
			Additions: 15 - i, // biggest first when ranked
		})
	}
	rec := &CommitRecord{
		Repo: "x", Scope: "x", SHA: "1", ShortSHA: "1",
		Subject: "s", Files: files,
	}
	ex := BuildCommitSummary(rec, 10)
	assistant := ex.Messages[1].Content
	if !strings.Contains(assistant, "5 more") {
		t.Errorf("overflow marker missing: %q", assistant)
	}
	// f0 had +15 (biggest), so it must appear; f14 had +1, must NOT appear.
	if !strings.Contains(assistant, "f0.go") {
		t.Errorf("biggest file missing: %q", assistant)
	}
	if strings.Contains(assistant, "f14.go") {
		t.Errorf("smallest file should have been capped: %q", assistant)
	}
}

func TestBuildFileSnapshot_shape(t *testing.T) {
	rec := &CommitRecord{
		Repo: "core-api", Scope: "backend", SHA: "abcd1234", ShortSHA: "abcd123",
		Subject: "fix(auth): rotate JWT key",
		Body:    "Per D17.",
	}
	snap := FileSnapshot{
		Path:        "src/Auth.java",
		Status:      "modified",
		Content:     "package auth;\nclass Auth { }\n",
		ContentSize: 28,
		DiffUnified: "diff --git a/src/Auth.java b/src/Auth.java\n@@ -1 +1,2 @@\n+package auth;\n class Auth { }\n",
		DiffSize:    100,
	}
	ex := BuildFileSnapshot(rec, snap)

	if ex.Family != "file-snapshot" {
		t.Errorf("family = %q", ex.Family)
	}
	if ex.Role != "domain" {
		t.Errorf("role = %q", ex.Role)
	}
	if ex.Source != "core-api@abcd123:src/Auth.java" {
		t.Errorf("source = %q", ex.Source)
	}
	if ex.Metadata["path"] != "src/Auth.java" {
		t.Errorf("path meta = %q", ex.Metadata["path"])
	}
	if ex.Metadata["status"] != "modified" {
		t.Errorf("status meta = %q", ex.Metadata["status"])
	}
	assistant := ex.Messages[1].Content
	if !strings.Contains(assistant, "Status: modified") {
		t.Errorf("status header missing: %q", assistant)
	}
	if !strings.Contains(assistant, "Content at this commit:") {
		t.Errorf("content section header missing")
	}
	if !strings.Contains(assistant, "Unified diff in this commit:") {
		t.Errorf("diff section header missing")
	}
	if !strings.Contains(assistant, "Commit context: fix(auth): rotate JWT key") {
		t.Errorf("commit context missing")
	}
}
