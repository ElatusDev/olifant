package dataset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripBannedLines_Nordstrom(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     string
		stripped int
	}{
		{
			name:     "nordstrom trailer",
			in:       "fix: reset\n\nCo-authored-by: x <x@nordstrom.com>\n",
			want:     "fix: reset\n\n",
			stripped: 1,
		},
		{
			name:     "nordstrom mid-content brand mention",
			in:       "context\nNordstrom artifactory mirror blocked init\nafter\n",
			want:     "context\nafter\n",
			stripped: 1,
		},
		{
			name:     "preserve unrelated content",
			in:       "regular commit message\nsecond line\n",
			want:     "regular commit message\nsecond line\n",
			stripped: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := stripBannedLines(tc.in)
			if got != tc.want {
				t.Errorf("string mismatch\n got: %q\nwant: %q", got, tc.want)
			}
			if n != tc.stripped {
				t.Errorf("strip count: got %d, want %d", n, tc.stripped)
			}
		})
	}
}

func TestStripBannedLines_Attribution(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     string
		stripped int
	}{
		{
			name:     "Claude commit trailer",
			in:       "feat: thing\n\nCo-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>\n",
			want:     "feat: thing\n\n",
			stripped: 1,
		},
		{
			name:     "robot-emoji generation marker",
			in:       "subject\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n",
			want:     "subject\n\n",
			stripped: 1,
		},
		{
			name:     "plain Generated with Claude Code",
			in:       "subject\n\nGenerated with Claude Code\n",
			want:     "subject\n\n",
			stripped: 1,
		},
		{
			name:     "two attribution lines back-to-back",
			in:       "x\n\nCo-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>\n🤖 Generated with [Claude Code](https://claude.com)\n",
			want:     "x\n\n",
			stripped: 2,
		},
		{
			name:     "preserve CLAUDE.md filename reference",
			in:       "see [CLAUDE.md](./CLAUDE.md) for onboarding",
			want:     "see [CLAUDE.md](./CLAUDE.md) for onboarding",
			stripped: 0,
		},
		{
			name:     "preserve claude-code CLI invocation",
			in:       "exec.Command(\"claude-code\", \"--plan\", path)",
			want:     "exec.Command(\"claude-code\", \"--plan\", path)",
			stripped: 0,
		},
		{
			name:     "preserve com.anthropic Maven group",
			in:       "<groupId>com.anthropic</groupId>",
			want:     "<groupId>com.anthropic</groupId>",
			stripped: 0,
		},
		{
			name:     "preserve prose mentioning Claude Code",
			in:       "We use Claude Code via the PSP executor to orchestrate plans",
			want:     "We use Claude Code via the PSP executor to orchestrate plans",
			stripped: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := stripBannedLines(tc.in)
			if got != tc.want {
				t.Errorf("string mismatch\n got: %q\nwant: %q", got, tc.want)
			}
			if n != tc.stripped {
				t.Errorf("strip count: got %d, want %d", n, tc.stripped)
			}
		})
	}
}

func TestSanitizeAnyRecursive(t *testing.T) {
	in := map[string]any{
		"system": "policy preamble\nCo-authored-by: x <x@nordstrom.com>\n",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "no attribution here",
			},
			map[string]any{
				"role":    "assistant",
				"content": "ok\n\nCo-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>\n",
			},
			map[string]any{
				"role":    "assistant",
				"content": "prelude\nbrand: Nordstrom artifactory mirror\nposture: locked\n",
			},
		},
		"metadata": map[string]any{
			"tag": "clean string referencing CLAUDE.md and claude-code subprocess",
		},
	}
	cleaned, n := sanitizeAny(in)
	if n != 3 {
		t.Fatalf("want 3 strips (1 nordstrom trailer + 1 Claude trailer + 1 nordstrom brand line), got %d", n)
	}
	out := cleaned.(map[string]any)
	bodyStr := mustJSON(t, out)
	if strings.Contains(strings.ToLower(bodyStr), "nordstrom") {
		t.Errorf("output still contains nordstrom: %s", bodyStr)
	}
	if strings.Contains(strings.ToLower(bodyStr), "noreply@anthropic.com") {
		t.Errorf("output still contains attribution email: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "CLAUDE.md") || !strings.Contains(bodyStr, "claude-code subprocess") {
		t.Errorf("functional refs were incorrectly removed: %s", bodyStr)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPackEndToEnd(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tier3-history")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rec1 := map[string]any{
		"system":   "sys",
		"messages": []any{map[string]any{"role": "assistant", "content": "feat\n\nCo-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>\n"}},
	}
	rec2 := map[string]any{
		"system":   "sys",
		"messages": []any{map[string]any{"role": "assistant", "content": "feat: clean message referencing CLAUDE.md"}},
	}
	rec3 := map[string]any{
		"system":   "sys",
		"messages": []any{map[string]any{"role": "assistant", "content": "pre\nNordstrom mirror line\npost\n"}},
	}
	var buf []byte
	for _, r := range []map[string]any{rec1, rec2, rec3} {
		b, _ := json.Marshal(r)
		buf = append(append(buf, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(sub, "a.jsonl"), buf, 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "packed.jsonl")
	stats, err := Pack(PackConfig{InputDir: dir, OutPath: outPath})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if stats.LinesIn != 3 || stats.LinesOut != 3 {
		t.Errorf("lines in/out: got %d/%d want 3/3", stats.LinesIn, stats.LinesOut)
	}
	if stats.LinesModified != 2 {
		t.Errorf("lines modified: got %d want 2", stats.LinesModified)
	}
	if stats.NordstromLinesStripped != 2 {
		t.Errorf("banned lines stripped: got %d want 2", stats.NordstromLinesStripped)
	}

	body, _ := os.ReadFile(outPath)
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "nordstrom") {
		t.Errorf("output still contains nordstrom")
	}
	if strings.Contains(lower, "noreply@anthropic.com") {
		t.Errorf("output still contains attribution email")
	}
	if !strings.Contains(string(body), "CLAUDE.md") {
		t.Errorf("functional CLAUDE.md reference was incorrectly removed")
	}
}

func TestPackRequiredArgs(t *testing.T) {
	if _, err := Pack(PackConfig{OutPath: "/tmp/x"}); err == nil {
		t.Error("expected error for missing InputDir")
	}
	if _, err := Pack(PackConfig{InputDir: t.TempDir()}); err == nil {
		t.Error("expected error for missing OutPath")
	}
}
