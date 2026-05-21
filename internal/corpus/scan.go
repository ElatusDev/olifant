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

// Scan dispatches to per-language extractors by file extension under
// cfg.SourceRoot. Day 1 added Java; Day 3 added TypeScript (.ts + .tsx).
// HCL / JSON / Markdown extractors land in Day 4-5.
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

	var symbols []Symbol

	// Java
	javaFiles, err := collectByExt(cfg.SourceRoot, ".java")
	if err != nil {
		return stats, fmt.Errorf("scan: walk %s for .java: %w", cfg.SourceRoot, err)
	}
	if cfg.Verbose && len(javaFiles) > 0 {
		fmt.Printf("  %d .java files under %s\n", len(javaFiles), cfg.SourceRoot)
	}
	for _, path := range javaFiles {
		rel, _ := filepath.Rel(cfg.RepoRoot, path)
		extracted, err := extractJava(path, rel, cfg)
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  WARN %s: %v\n", rel, err)
			}
			continue
		}
		symbols = append(symbols, extracted...)
		stats.FilesScanned++
	}

	// TypeScript (.ts + .tsx; test files filtered out by isTestFile)
	tsFiles, err := collectByExts(cfg.SourceRoot, ".ts", ".tsx")
	if err != nil {
		return stats, fmt.Errorf("scan: walk %s for .ts/.tsx: %w", cfg.SourceRoot, err)
	}
	var tsKept []string
	for _, p := range tsFiles {
		if !isTestFile(p) {
			tsKept = append(tsKept, p)
		}
	}
	if cfg.Verbose && len(tsFiles) > 0 {
		fmt.Printf("  %d .ts/.tsx files under %s (%d excluded as tests)\n",
			len(tsKept), cfg.SourceRoot, len(tsFiles)-len(tsKept))
	}
	for _, path := range tsKept {
		rel, _ := filepath.Rel(cfg.RepoRoot, path)
		extracted, err := extractTypeScript(path, rel, cfg)
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

// collectByExt walks root and returns every regular file whose name has
// the given extension. Skips common artefact dirs.
func collectByExt(root, ext string) ([]string, error) {
	return collectByExts(root, ext)
}

// collectByExts is collectByExt for multiple extensions in a single walk.
// Skips the same artefact dirs plus webapp/mobile-specific build outputs.
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
