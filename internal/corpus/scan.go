package corpus

// Scan = v2 curriculum vocabulary extractor. Co-exists with Build
// (v1 corpus builder, in builder.go).

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// extractFunc is the per-language extractor signature.
type extractFunc func(absPath, relPath string, cfg ScanConfig) ([]Symbol, error)

// repoExtractProfile is the dispatch entry for one repo: which file
// extensions to walk, which extractor to call, and an optional
// per-path exclude predicate (test files, backup files, etc.).
type repoExtractProfile struct {
	exts    []string
	extract extractFunc
	exclude func(path string) bool
}

func repoProfile(repo string) (repoExtractProfile, bool) {
	switch repo {
	case "core-api":
		return repoExtractProfile{exts: []string{".java"}, extract: extractJava}, true
	case "akademia-plus-web", "elatusdev-web", "akademia-plus-central", "akademia-plus-go":
		return repoExtractProfile{exts: []string{".ts", ".tsx"}, extract: extractTypeScript, exclude: isTestFile}, true
	case "infra":
		return repoExtractProfile{exts: []string{".tf"}, extract: extractHCL}, true
	case "core-api-e2e":
		return repoExtractProfile{exts: []string{".json"}, extract: extractPostman, exclude: isPostmanBackup}, true
	case "knowledge-base":
		return repoExtractProfile{exts: []string{".md", ".yaml"}, extract: extractKB, exclude: isKBNonCurated}, true
	}
	return repoExtractProfile{}, false
}

// Scan dispatches to a per-repo extractor profile (see repoProfile),
// walks SourceRoot for that profile's file extensions, runs the
// extractor per file, and writes the aggregated Symbols as YAML.
func Scan(cfg ScanConfig) (ScanStats, error) {
	started := time.Now()
	stats := ScanStats{ByKind: map[string]int{}, ByConcern: map[string]int{}}

	if cfg.RepoRoot == "" {
		return stats, fmt.Errorf("scan: RepoRoot required")
	}
	if cfg.SourceRoot == "" {
		return stats, fmt.Errorf("scan: SourceRoot required")
	}
	if cfg.OutPath == "" && !cfg.DryRun {
		return stats, fmt.Errorf("scan: OutPath required unless DryRun")
	}

	prof, ok := repoProfile(cfg.Repo)
	if !ok {
		return stats, fmt.Errorf("scan: no extractor profile for repo %q", cfg.Repo)
	}

	files, err := collectByExts(cfg.SourceRoot, prof.exts...)
	if err != nil {
		return stats, fmt.Errorf("scan: walk %s for %v: %w", cfg.SourceRoot, prof.exts, err)
	}
	var kept []string
	for _, p := range files {
		if prof.exclude != nil && prof.exclude(p) {
			continue
		}
		kept = append(kept, p)
	}
	if cfg.Verbose && len(files) > 0 {
		fmt.Printf("  %d file(s) of %v under %s (%d excluded)\n",
			len(kept), prof.exts, cfg.SourceRoot, len(files)-len(kept))
	}

	var symbols []Symbol
	for _, path := range kept {
		rel, _ := filepath.Rel(cfg.RepoRoot, path)
		extracted, err := prof.extract(path, rel, cfg)
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  WARN %s: %v\n", rel, err)
			}
			continue
		}
		symbols = append(symbols, extracted...)
		stats.FilesScanned++
	}

	for _, s := range symbols {
		stats.SymbolsEmitted++
		if k, ok := s.Tags[AxisKind].(string); ok {
			stats.ByKind[k]++
		}
		if cs, ok := s.Tags[AxisConcern].([]string); ok {
			for _, c := range cs {
				stats.ByConcern[c]++
			}
		}
	}

	stats.Elapsed = time.Since(started)

	if cfg.DryRun {
		return stats, nil
	}

	if err := os.MkdirAll(filepath.Dir(cfg.OutPath), 0o755); err != nil {
		return stats, fmt.Errorf("scan: mkdir out parent: %w", err)
	}
	out, err := os.Create(cfg.OutPath)
	if err != nil {
		return stats, fmt.Errorf("scan: create out: %w", err)
	}
	defer out.Close()
	enc := yaml.NewEncoder(out)
	enc.SetIndent(2)
	if err := enc.Encode(symbols); err != nil {
		return stats, fmt.Errorf("scan: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return stats, fmt.Errorf("scan: close encoder: %w", err)
	}
	return stats, nil
}

// collectByExts walks root and returns every regular file whose name
// has any of the given extensions. Skips common artefact dirs plus
// webapp / mobile-specific build outputs.
func collectByExts(root string, exts ...string) ([]string, error) {
	skip := map[string]struct{}{
		".git": {}, "node_modules": {}, "target": {}, "build": {},
		"dist": {}, "bin": {}, "vendor": {}, ".gradle": {}, ".idea": {},
		".next": {}, ".expo": {}, "coverage": {},
	}
	extSet := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		extSet[strings.ToLower(e)] = struct{}{}
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, ok := skip[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(d.Name())
		for ext := range extSet {
			if strings.HasSuffix(lower, ext) {
				out = append(out, path)
				return nil
			}
		}
		return nil
	})
	return out, err
}

// symbolID hashes (source, line, text) into a short stable ID.
func symbolID(source string, line int, text string) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%s:%d:%s", source, line, text)
	return "SYM-" + hex.EncodeToString(h.Sum(nil))[:12]
}
