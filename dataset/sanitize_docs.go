package dataset

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SanitizeDocsConfig drives `olifant dataset sanitize-docs` — walk a
// directory tree, strip Claude/Anthropic and nordstrom attribution lines
// from every *.md file. Functional references (CLAUDE.md filename,
// claude-code CLI, com.anthropic Maven group, prose mentions) are
// deliberately preserved.
type SanitizeDocsConfig struct {
	Root    string   // directory to walk (required)
	Exclude []string // dir basenames to skip (defaults to skipDirsDefault)
	DryRun  bool     // print what would change; do not write
	Verbose bool
}

// SanitizeDocsStats summarises one sweep run.
type SanitizeDocsStats struct {
	FilesScanned  int
	FilesModified int
	LinesStripped int
	BytesBefore   int64
	BytesAfter    int64
	Elapsed       time.Duration
	PerFile       map[string]int // path -> lines stripped (only files actually changed)
}

// skipDirsDefault are dirs we never descend into — build artifacts +
// vendor + VCS state.
var skipDirsDefault = []string{
	".git", "node_modules", "vendor", "target", "bin", "dist",
	"build", ".next", ".cache", ".gradle", ".idea", ".vscode",
	"out", "coverage",
}

// docAttributionPatterns are the attribution shapes stripped from
// markdown documents. Each pattern requires the offending token at the
// start of a line (whitespace-only leading) so descriptive prose like
// "the Co-Authored-By trailer should…" or "training JSONL MUST have
// `Co-authored-by: …@nordstrom.com` lines stripped" is preserved as
// documentation; only literal trailer/footer lines are removed.
var docAttributionPatterns = []*regexp.Regexp{
	// Claude commit-trailer attribution
	regexp.MustCompile(`(?im)^[ \t]*co-?authored-?by:[ \t]*[^<\n]*claude[^\n]*\n?`),
	// Nordstrom-email commit-trailer attribution
	regexp.MustCompile(`(?im)^[ \t]*co-?authored-?by:[^\n]*@nordstrom\.com[^\n]*\n?`),
	// Robot-emoji generation marker
	regexp.MustCompile(`(?im)^[ \t]*🤖[ \t]*generated[ \t]+with[^\n]*\n?`),
	// Text-only generation marker (claude code variants)
	regexp.MustCompile(`(?im)^[ \t]*generated[ \t]+with[ \t]+\[?claude[ \t]+code[^\n]*\n?`),
	// Anthropic-signed email (standalone — embedded mentions inside prose are kept)
	regexp.MustCompile(`(?im)^[ \t]*<?noreply@anthropic\.com>?[ \t]*$\n?`),
}

// stripDocAttribution applies every docAttributionPatterns regex to s
// and returns the cleaned string + lines stripped.
func stripDocAttribution(s string) (string, int) {
	total := 0
	for _, re := range docAttributionPatterns {
		matches := re.FindAllStringIndex(s, -1)
		if len(matches) == 0 {
			continue
		}
		s = re.ReplaceAllString(s, "")
		total += len(matches)
	}
	return s, total
}

// SanitizeDocs walks Root and rewrites every *.md file in place,
// stripping attribution lines. With DryRun=true, no writes occur.
func SanitizeDocs(cfg SanitizeDocsConfig) (SanitizeDocsStats, error) {
	started := time.Now()
	stats := SanitizeDocsStats{PerFile: map[string]int{}}

	if cfg.Root == "" {
		return stats, fmt.Errorf("sanitize-docs: Root required")
	}
	exclude := cfg.Exclude
	if len(exclude) == 0 {
		exclude = skipDirsDefault
	}
	excludeSet := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		excludeSet[e] = struct{}{}
	}

	err := filepath.WalkDir(cfg.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := excludeSet[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		stats.FilesScanned++

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		stats.BytesBefore += int64(len(data))

		cleaned, n := stripDocAttribution(string(data))
		if n == 0 || bytes.Equal([]byte(cleaned), data) {
			stats.BytesAfter += int64(len(data))
			return nil
		}

		stats.FilesModified++
		stats.LinesStripped += n
		stats.PerFile[path] = n
		stats.BytesAfter += int64(len(cleaned))

		if cfg.Verbose {
			fmt.Printf("  %s  -%d line(s)\n", path, n)
		}
		if cfg.DryRun {
			return nil
		}
		if err := os.WriteFile(path, []byte(cleaned), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	// Sort PerFile keys for stable downstream listing.
	paths := make([]string, 0, len(stats.PerFile))
	for p := range stats.PerFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	stats.Elapsed = time.Since(started)
	return stats, nil
}
