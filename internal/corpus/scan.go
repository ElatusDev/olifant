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

// Scan dispatches to a per-language extractor based on cfg.Repo and the
// file extensions under cfg.SourceRoot. For Day 1 the Java extractor is
// the only one implemented; other extractors return ErrNotImplemented.
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

	// Collect source files by language under SourceRoot.
	javaFiles, err := collectByExt(cfg.SourceRoot, ".java")
	if err != nil {
		return stats, fmt.Errorf("scan: walk %s: %w", cfg.SourceRoot, err)
	}
	if cfg.Verbose {
		fmt.Printf("  %d .java files under %s\n", len(javaFiles), cfg.SourceRoot)
	}

	var symbols []Symbol
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
	var skip = map[string]struct{}{
		".git": {}, "node_modules": {}, "target": {}, "build": {},
		"dist": {}, "bin": {}, "vendor": {}, ".gradle": {}, ".idea": {},
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
		if strings.HasSuffix(strings.ToLower(d.Name()), strings.ToLower(ext)) {
			out = append(out, path)
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
