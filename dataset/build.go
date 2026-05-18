package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Build orchestrates one dataset-extraction run. It dispatches to
// each requested source's extractor, writes per-source JSONL groups,
// and persists a manifest under cfg.OutDir.
func Build(cfg BuildConfig) (BuildStats, error) {
	start := time.Now()
	stats := BuildStats{PerSource: map[string]SourceStats{}}

	if cfg.KBRoot == "" {
		return stats, fmt.Errorf("BuildConfig.KBRoot is required")
	}
	if cfg.OutDir == "" {
		return stats, fmt.Errorf("BuildConfig.OutDir is required")
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = append([]SourceKind(nil), AllSources...)
	}

	// Deterministic ordering matches AllSources.
	sources := dedupeAndOrder(cfg.Sources)

	for _, src := range sources {
		exs, srcStats, err := runOne(cfg.KBRoot, src)
		if err != nil {
			return stats, fmt.Errorf("source %s: %w", src, err)
		}
		stats.SourcesProcessed++
		stats.PerSource[string(src)] = srcStats
		stats.ExamplesEmitted += srcStats.ExamplesEmitted

		if cfg.WriteJSONL && len(exs) > 0 {
			subdir := filepath.Join(cfg.OutDir, src.SubDir())
			if _, _, err := emitJSONL(subdir, exs); err != nil {
				return stats, fmt.Errorf("emit %s: %w", src, err)
			}
		}

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  %-14s files=%d entries=%d examples=%d\n",
				src, srcStats.FilesScanned, srcStats.EntriesParsed, srcStats.ExamplesEmitted)
		}
	}

	stats.Elapsed = time.Since(start)

	if cfg.WriteJSONL {
		if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
			return stats, fmt.Errorf("mkdir %s: %w", cfg.OutDir, err)
		}
		m := &Manifest{
			RunID:          nowUTC().Format("2006-01-02T15-04-05Z") + "-dataset",
			BuilderVersion: BuilderVersion,
			GeneratedAt:    nowUTC().Format(time.RFC3339),
			KBRoot:         cfg.KBRoot,
			OutDir:         cfg.OutDir,
			Sources:        stringerStrings(sources),
			Totals: ManifestTotals{
				SourcesProcessed: stats.SourcesProcessed,
				ExamplesEmitted:  stats.ExamplesEmitted,
				ElapsedMs:        int(stats.Elapsed.Milliseconds()),
			},
			PerSource: stats.PerSource,
		}
		if err := writeManifest(cfg.OutDir, m); err != nil {
			return stats, err
		}
	}

	return stats, nil
}

// runOne dispatches to the per-source extractor. Returning extractor
// matches `(examples, stats, err)` so Build's loop is uniform.
func runOne(kbRoot string, src SourceKind) ([]Example, SourceStats, error) {
	switch src {
	case SourceRetros:
		return ExtractRetros(kbRoot)
	case SourceDecisions:
		return ExtractDecisions(kbRoot)
	case SourceAntipatterns:
		return ExtractAntipatterns(kbRoot)
	case SourcePatterns:
		return ExtractPatterns(kbRoot)
	case SourceTriples:
		return ExtractTriples(kbRoot)
	default:
		return nil, SourceStats{}, fmt.Errorf("unknown source %q", src)
	}
}

// dedupeAndOrder returns a deterministic ordering of the requested
// sources, preserving AllSources canonical order and removing dupes.
func dedupeAndOrder(reqs []SourceKind) []SourceKind {
	want := map[SourceKind]bool{}
	for _, s := range reqs {
		want[s] = true
	}
	var out []SourceKind
	for _, s := range AllSources {
		if want[s] {
			out = append(out, s)
		}
	}
	// Anything requested but not canonical (typo / future kind):
	// pass through at end for visibility, sorted for determinism.
	extras := []string{}
	canonical := map[SourceKind]bool{}
	for _, s := range AllSources {
		canonical[s] = true
	}
	for s := range want {
		if !canonical[s] {
			extras = append(extras, string(s))
		}
	}
	sort.Strings(extras)
	for _, e := range extras {
		out = append(out, SourceKind(e))
	}
	return out
}

func stringerStrings(xs []SourceKind) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}
