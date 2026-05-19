package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripDocAttribution(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     string
		stripped int
	}{
		{
			name:     "Claude commit trailer (one line matches two patterns)",
			in:       "# title\n\nbody\n\nCo-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>\n",
			want:     "# title\n\nbody\n\n",
			stripped: 1, // single line stripped by the first matching pattern
		},
		{
			name:     "nordstrom email trailer (attribution only)",
			in:       "# retro\n\ncontext\n\nCo-authored-by: name <name@nordstrom.com>\n",
			want:     "# retro\n\ncontext\n\n",
			stripped: 1,
		},
		{
			name:     "preserve nordstrom brand mention (not attribution)",
			in:       "# retro\n\nNordstrom artifactory mirror blocked init\n\nremediation steps below\n",
			want:     "# retro\n\nNordstrom artifactory mirror blocked init\n\nremediation steps below\n",
			stripped: 0,
		},
		{
			name:     "robot-emoji generation footer",
			in:       "# subject\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n",
			want:     "# subject\n\n",
			stripped: 1,
		},
		{
			name:     "Generated with Claude Code text marker",
			in:       "Generated with Claude Code on 2026-05-19\n\nrest of doc\n",
			want:     "\nrest of doc\n",
			stripped: 1,
		},
		{
			name:     "preserve CLAUDE.md filename reference",
			in:       "see [CLAUDE.md](./CLAUDE.md) for project onboarding\n",
			want:     "see [CLAUDE.md](./CLAUDE.md) for project onboarding\n",
			stripped: 0,
		},
		{
			name:     "preserve claude-code CLI reference",
			in:       "Run `claude-code` to start the agent\n",
			want:     "Run `claude-code` to start the agent\n",
			stripped: 0,
		},
		{
			name:     "preserve prose mentioning Claude Code",
			in:       "We orchestrate via Claude Code in PSP mode\n",
			want:     "We orchestrate via Claude Code in PSP mode\n",
			stripped: 0,
		},
		{
			name:     "preserve descriptive prose explaining the attribution rule",
			in:       "training JSONL MUST have `Co-authored-by: ...@nordstrom.com` lines stripped\n",
			want:     "training JSONL MUST have `Co-authored-by: ...@nordstrom.com` lines stripped\n",
			stripped: 0,
		},
		{
			name:     "preserve retro narrative discussing past commits",
			in:       "Commits used `Co-Authored-By: Claude Opus 4.6` trailer + Nordstrom-identity author\n",
			want:     "Commits used `Co-Authored-By: Claude Opus 4.6` trailer + Nordstrom-identity author\n",
			stripped: 0,
		},
		{
			name:     "preserve com.anthropic Maven group reference",
			in:       "Add `com.anthropic:java-sdk:1.0.0` to deps\n",
			want:     "Add `com.anthropic:java-sdk:1.0.0` to deps\n",
			stripped: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := stripDocAttribution(tc.in)
			if got != tc.want {
				t.Errorf("string mismatch\n got: %q\nwant: %q", got, tc.want)
			}
			if n != tc.stripped {
				t.Errorf("strip count: got %d, want %d", n, tc.stripped)
			}
		})
	}
}

func TestSanitizeDocsWalk(t *testing.T) {
	root := t.TempDir()
	// Layout:
	//   root/a.md (modified)
	//   root/sub/b.md (modified)
	//   root/sub/clean.md (no change)
	//   root/.git/inside.md (should be skipped via .git exclude)
	//   root/code.go (skipped: not .md)
	must := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "a.md"), "# a\nco-authored-by: Claude Opus 4.7 <noreply@anthropic.com>\n")
	must(filepath.Join(root, "sub", "b.md"), "# b\n🤖 Generated with [Claude Code](https://claude.com)\nbody\n")
	must(filepath.Join(root, "sub", "clean.md"), "# c\n## section\nno attribution here\n")
	must(filepath.Join(root, ".git", "inside.md"), "co-authored-by: Claude <noreply@anthropic.com>\n")
	must(filepath.Join(root, "code.go"), "// package code\n")

	stats, err := SanitizeDocs(SanitizeDocsConfig{Root: root})
	if err != nil {
		t.Fatalf("SanitizeDocs: %v", err)
	}
	if stats.FilesScanned != 3 {
		t.Errorf("files scanned: got %d want 3 (a.md + b.md + clean.md; .git excluded; code.go non-md)", stats.FilesScanned)
	}
	if stats.FilesModified != 2 {
		t.Errorf("files modified: got %d want 2", stats.FilesModified)
	}
	if stats.LinesStripped != 2 {
		t.Errorf("lines stripped: got %d want 2 (1 trailer line in a.md + 1 robot-emoji in b.md)", stats.LinesStripped)
	}

	a, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if strings.Contains(strings.ToLower(string(a)), "noreply@anthropic.com") {
		t.Errorf("a.md still contains attribution: %s", a)
	}
	c, _ := os.ReadFile(filepath.Join(root, "sub", "clean.md"))
	if string(c) != "# c\n## section\nno attribution here\n" {
		t.Errorf("clean.md was mutated: %s", c)
	}
	g, _ := os.ReadFile(filepath.Join(root, ".git", "inside.md"))
	if !strings.Contains(strings.ToLower(string(g)), "noreply@anthropic.com") {
		t.Errorf(".git was not excluded — inside.md should be untouched")
	}
}

func TestSanitizeDocsDryRun(t *testing.T) {
	root := t.TempDir()
	original := "Co-Authored-By: Claude <noreply@anthropic.com>\n"
	p := filepath.Join(root, "a.md")
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := SanitizeDocs(SanitizeDocsConfig{Root: root, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesModified != 1 || stats.LinesStripped != 1 {
		t.Errorf("dry-run stats wrong: modified=%d stripped=%d", stats.FilesModified, stats.LinesStripped)
	}
	got, _ := os.ReadFile(p)
	if string(got) != original {
		t.Errorf("dry-run mutated file: %s", got)
	}
}
