package history

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Scan walks each repo's commit history, builds CommitRecords with
// per-file snapshots, and optionally emits two JSONL families per
// repo. Incremental mode is on by default: the manifest at
// cfg.ManifestPath records last_sha per repo, and only commits newer
// than that are walked. Use cfg.FullScan=true to ignore the manifest.
//
// Phase 1+2 — no ChromaDB writes. Phase 3 adds the embed + upsert step.
func Scan(ctx context.Context, cfg ScanConfig) (ScanStats, error) {
	stats := ScanStats{
		PerRepo:  map[string]int{},
		PerScope: map[string]int{},
	}
	start := time.Now()

	if cfg.ContentCapBytes <= 0 {
		cfg.ContentCapBytes = DefaultContentCapBytes
	}
	if cfg.DiffCapBytes <= 0 {
		cfg.DiffCapBytes = DefaultDiffCapBytes
	}
	if cfg.FilesListCap <= 0 {
		cfg.FilesListCap = DefaultFilesListCap
	}
	if cfg.Since.IsZero() {
		cfg.Since = DefaultSince
	}

	// Load incremental manifest. First-run produces an empty one.
	var manifest *Manifest
	if cfg.ManifestPath != "" && !cfg.FullScan {
		m, err := LoadManifest(cfg.ManifestPath)
		if err != nil {
			return stats, err
		}
		manifest = m
	} else {
		manifest = &Manifest{BuilderVersion: BuilderVersion}
	}

	for _, rs := range cfg.Repos {
		if _, err := os.Stat(rs.Path); err != nil {
			if cfg.Verbose {
				fmt.Printf("  skip %s: %v\n", rs.Name, err)
			}
			continue
		}

		stopAtSHA := ""
		if !cfg.FullScan {
			stopAtSHA = manifest.LastSHA(rs.Name)
		}

		records, walked, err := Walk(ctx, rs.Path, rs.Name, rs.Scope, stopAtSHA, cfg)
		stats.CommitsWalked += walked
		if err != nil {
			return stats, fmt.Errorf("walk %s: %w", rs.Name, err)
		}

		stats.CommitsEmitted += len(records)
		stats.CommitsSkipped += walked - len(records)
		stats.PerRepo[rs.Name] = len(records)
		stats.PerScope[rs.Scope] += len(records)

		snapsThisRepo := 0
		for _, r := range records {
			for _, s := range r.Snapshots {
				if s.ContentTruncated {
					stats.SnapshotsTruncated++
				}
			}
			snapsThisRepo += len(r.Snapshots)
		}

		if cfg.Verbose {
			tag := "full-scan"
			if stopAtSHA != "" {
				tag = "incr (since " + stopAtSHA[:7] + ")"
			}
			fmt.Printf("  %-22s walked=%-5d commits=%-5d snapshots=%-6d scope=%s  [%s]\n",
				rs.Name, walked, len(records), snapsThisRepo, rs.Scope, tag)
		}

		if cfg.WriteJSONL && !cfg.DryRun && cfg.OutDir != "" && len(records) > 0 {
			cRows, sRows, werr := EmitJSONL(cfg.OutDir, rs.Name, records, cfg.FilesListCap)
			if werr != nil {
				return stats, werr
			}
			stats.SnapshotsEmitted += sRows
			if cfg.Verbose {
				fmt.Printf("    wrote %s.commits.jsonl (%d) + %s.snapshots.jsonl (%d)\n",
					rs.Name, cRows, rs.Name, sRows)
			}
		} else {
			stats.SnapshotsEmitted += snapsThisRepo
		}

		// Update manifest for this repo if we emitted anything.
		// records is newest-first (LogOrderCommitterTime); records[0]
		// is the latest commit we just processed.
		if len(records) > 0 {
			newest := records[0]
			manifest.UpdateRepo(rs.Name, rs.Scope, newest.SHA, newest.CommittedAt,
				RunDelta{CommitsAdded: len(records), SnapshotsAdded: snapsThisRepo})
		}

		stats.ReposProcessed++
	}

	stats.Elapsed = time.Since(start)

	// Persist manifest (incremental scan handshake for next retrain).
	if cfg.ManifestPath != "" && cfg.WriteManifest && !cfg.DryRun {
		manifest.LastRunAt = time.Now().UTC().Format(time.RFC3339)
		manifest.SinceFloor = cfg.Since.Format("2006-01-02")
		manifest.BuilderVersion = BuilderVersion
		if err := SaveManifest(cfg.ManifestPath, manifest); err != nil {
			return stats, err
		}
		if cfg.Verbose {
			fmt.Printf("  wrote manifest %s\n", cfg.ManifestPath)
		}
	}

	return stats, nil
}
