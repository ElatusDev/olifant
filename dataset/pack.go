package dataset

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PackConfig drives `olifant dataset pack` — concatenate per-tier
// JSONLs from a dataset-build output dir into one ShareGPT JSONL,
// stripping any `Co-authored-by: …@nordstrom.com` attribution lines
// embedded in string fields (per olifant-fine-tune-v1-prompt.md §1
// Hard Rule #4).
type PackConfig struct {
	InputDir string   // training dir (e.g. <kb-root>/training/2026-05-18)
	OutPath  string   // single concatenated JSONL output
	Subdirs  []string // top-level dirs to include; empty = all
	Verbose  bool
}

// PackStats summarises one pack run. NordstromLinesStripped is preserved
// as the user-visible counter even though the strip now also removes
// Claude/Anthropic attribution lines — the name is kept for backwards
// compat with the §6 Execution Log schema; both pattern families count
// against it.
type PackStats struct {
	FilesScanned           int
	LinesIn                int
	LinesOut               int
	LinesModified          int
	NordstromLinesStripped int
	BytesOut               int64
	Elapsed                time.Duration
	PerFile                map[string]int
}

// nordstromLineRE matches any line containing "nordstrom" (case-insensitive)
// inside a multi-line string field. Per the no-leak choice for the
// former-employer brand: strip both trailer-position `Co-authored-by:
// …@nordstrom.com` lines AND mid-content brand mentions (artifactory
// mirror, git-identity-split context).
var nordstromLineRE = regexp.MustCompile(`(?im)^[^\n]*nordstrom[^\n]*\n?`)

// attributionLineRE matches Claude/Anthropic *attribution* lines only —
// commit trailers, generation footers, robot-emoji markers, and the
// signing email. Functional references (`CLAUDE.md` filename,
// `claude-code` CLI subprocess, `com.anthropic` Maven group, prose
// describing the toolchain) are deliberately NOT matched. Aligns with
// global CLAUDE.md attribution policy.
var attributionLineRE = regexp.MustCompile(
	`(?im)^[^\n]*(co-?authored-?by:[ \t]*[^<\n]*claude|🤖[ \t]*generated[ \t]+with|generated[ \t]+with[ \t]+\[?claude[ \t]+code|noreply@anthropic\.com)[^\n]*\n?`,
)

// stripPatterns is the ordered list of regexes applied to every string
// leaf inside a training record.
var stripPatterns = []*regexp.Regexp{nordstromLineRE, attributionLineRE}

// stripBannedLines applies every stripPatterns regex to s, returning
// the cleaned string and the total number of lines removed.
func stripBannedLines(s string) (string, int) {
	total := 0
	for _, re := range stripPatterns {
		matches := re.FindAllStringIndex(s, -1)
		if len(matches) == 0 {
			continue
		}
		s = re.ReplaceAllString(s, "")
		total += len(matches)
	}
	return s, total
}

// sanitizeAny recursively walks val and strips nordstrom attribution from
// every string leaf. Returns the (possibly mutated) value and total lines
// stripped across the tree.
func sanitizeAny(val any) (any, int) {
	switch v := val.(type) {
	case string:
		cleaned, n := stripBannedLines(v)
		return cleaned, n
	case map[string]any:
		total := 0
		for k, child := range v {
			newChild, n := sanitizeAny(child)
			v[k] = newChild
			total += n
		}
		return v, total
	case []any:
		total := 0
		for i, child := range v {
			newChild, n := sanitizeAny(child)
			v[i] = newChild
			total += n
		}
		return v, total
	default:
		return val, 0
	}
}

// Pack walks InputDir, concatenates all *.jsonl files (recursively), strips
// nordstrom Co-authored-by lines from any string field, and writes the
// concatenated result to OutPath. JSONL record count is preserved — only
// string content is modified.
func Pack(cfg PackConfig) (PackStats, error) {
	started := time.Now()
	stats := PackStats{PerFile: map[string]int{}}

	if cfg.InputDir == "" {
		return stats, fmt.Errorf("pack: InputDir required")
	}
	if cfg.OutPath == "" {
		return stats, fmt.Errorf("pack: OutPath required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.OutPath), 0o755); err != nil {
		return stats, fmt.Errorf("pack: mkdir out parent: %w", err)
	}

	out, err := os.Create(cfg.OutPath)
	if err != nil {
		return stats, fmt.Errorf("pack: create out: %w", err)
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)

	files, err := collectJSONL(cfg.InputDir, cfg.Subdirs)
	if err != nil {
		return stats, err
	}
	stats.FilesScanned = len(files)

	for _, path := range files {
		n, err := packFile(path, bw, &stats)
		if err != nil {
			return stats, fmt.Errorf("pack: %s: %w", path, err)
		}
		stats.PerFile[path] = n
		if cfg.Verbose {
			fmt.Printf("  %s  %d lines\n", path, n)
		}
	}

	if err := bw.Flush(); err != nil {
		return stats, err
	}
	if fi, _ := out.Stat(); fi != nil {
		stats.BytesOut = fi.Size()
	}
	stats.Elapsed = time.Since(started)
	return stats, nil
}

func collectJSONL(root string, subdirs []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if len(subdirs) > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
			keep := false
			for _, sd := range subdirs {
				if top == sd {
					keep = true
					break
				}
			}
			if !keep {
				return nil
			}
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pack: walk %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

func packFile(path string, w io.Writer, stats *PackStats) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24) // tier3-history records can exceed 64KB
	count := 0
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		stats.LinesIn++
		var obj any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return count, fmt.Errorf("line %d: %w", stats.LinesIn, err)
		}
		cleaned, stripped := sanitizeAny(obj)
		if stripped > 0 {
			stats.LinesModified++
			stats.NordstromLinesStripped += stripped
		}
		encoded, err := json.Marshal(cleaned)
		if err != nil {
			return count, fmt.Errorf("line %d marshal: %w", stats.LinesIn, err)
		}
		if _, err := w.Write(encoded); err != nil {
			return count, err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return count, err
		}
		stats.LinesOut++
		count++
	}
	return count, sc.Err()
}
