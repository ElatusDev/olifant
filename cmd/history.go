package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/history"
)

// History dispatches `olifant history <scan|...>`. Phase 1 ships
// `scan` only; `index` (ChromaDB) + `stats` (manifest) follow in
// later phases per the track-4 phasing.
func History(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant history: missing action (scan)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "scan":
		return historyScan(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant history: unknown action %q\n", action)
		return 2
	}
}

func historyScan(args []string) int {
	fs := flag.NewFlagSet("history scan", flag.ExitOnError)
	platformRoot := fs.String("platform-root", "", "platform root containing the 7 repos (default: parent of kb-root)")
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect via cwd ancestors)")
	out := fs.String("out", "", "JSONL output dir (default: <kb-root>/training/<YYYY-MM-DD>/tier3-history)")
	repoFilter := fs.String("repo", "", "comma-separated repo names to scan (default: all 7)")
	sinceFlag := fs.String("since", "2026-01-01", "ISO date floor for committer-time (YYYY-MM-DD)")
	contentCap := fs.Int("content-cap", history.DefaultContentCapBytes, "max bytes of file content per snapshot (truncate beyond)")
	diffCap := fs.Int("diff-cap", history.DefaultDiffCapBytes, "max bytes of unified diff per file (truncate beyond)")
	filesListCap := fs.Int("files-list-cap", history.DefaultFilesListCap, "max files in commit-summary listing (overflow as 'N more')")
	manifestPath := fs.String("manifest", "", "incremental-scan manifest path (default: <kb-root>/short-term/history-manifest.yaml)")
	fullScan := fs.Bool("full-scan", false, "ignore manifest last_sha; re-walk every commit since --since")
	noManifestUpdate := fs.Bool("no-manifest-update", false, "do not write the manifest back after a successful scan")
	verbose := fs.Bool("v", false, "verbose progress")
	dryRun := fs.Bool("dry-run", false, "walk + parse only; no JSONL write")
	timeoutSec := fs.Int("timeout", 3600, "overall timeout in seconds (default: 60 min)")
	_ = fs.Parse(args)

	since, err := time.Parse("2006-01-02", *sinceFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "history scan: --since must be YYYY-MM-DD: %v\n", err)
		return 2
	}

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		}
	}
	if root != "" {
		root, _ = filepath.Abs(root)
	}

	pr := *platformRoot
	if pr == "" && root != "" {
		pr = filepath.Dir(root)
	}
	if pr == "" {
		fmt.Fprintln(os.Stderr, "history scan: --platform-root not specified and kb-root not found")
		return 1
	}
	pr, _ = filepath.Abs(pr)

	outDir := *out
	if outDir == "" && root != "" {
		outDir = filepath.Join(root, "training", time.Now().UTC().Format("2006-01-02"), "tier3-history")
	}

	manPath := *manifestPath
	if manPath == "" && root != "" {
		manPath = filepath.Join(root, "short-term", "history-manifest.yaml")
	}

	allRepos := history.DefaultRepos(pr)
	var selected []history.RepoSpec
	if *repoFilter != "" {
		wanted := map[string]bool{}
		for _, n := range strings.Split(*repoFilter, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				wanted[n] = true
			}
		}
		for _, r := range allRepos {
			if wanted[r.Name] {
				selected = append(selected, r)
			}
		}
	} else {
		selected = allRepos
	}
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "history scan: no repos selected")
		return 1
	}

	fmt.Println("platform-root:", pr)
	fmt.Println("out:          ", outDir)
	fmt.Println("manifest:     ", manPath)
	fmt.Println("since:        ", since.Format("2006-01-02"))
	fmt.Println("mode:         ", scanMode(*fullScan))
	fmt.Println("repos:")
	for _, r := range selected {
		fmt.Printf("  - %-22s [%s]  %s\n", r.Name, r.Scope, r.Path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	stats, err := history.Scan(ctx, history.ScanConfig{
		Repos:           selected,
		Since:           since,
		OutDir:          outDir,
		WriteJSONL:      !*dryRun,
		ContentCapBytes: *contentCap,
		DiffCapBytes:    *diffCap,
		FilesListCap:    *filesListCap,
		ManifestPath:    manPath,
		FullScan:        *fullScan,
		WriteManifest:   !*noManifestUpdate,
		Verbose:         *verbose,
		DryRun:          *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "history scan:", err)
		return 1
	}

	fmt.Println("history scan summary:")
	fmt.Printf("  repos processed:    %d\n", stats.ReposProcessed)
	fmt.Printf("  commits walked:     %d\n", stats.CommitsWalked)
	fmt.Printf("  commits emitted:    %d\n", stats.CommitsEmitted)
	fmt.Printf("  commits skipped:    %d (no-parent / parse-fail)\n", stats.CommitsSkipped)
	fmt.Printf("  snapshots emitted:  %d (one per (commit, file))\n", stats.SnapshotsEmitted)
	fmt.Printf("  snapshots truncated:%d (file content > content-cap)\n", stats.SnapshotsTruncated)
	fmt.Printf("  elapsed:            %s\n", stats.Elapsed.Round(time.Millisecond))

	if len(stats.PerRepo) > 0 {
		fmt.Println("  per repo:")
		keys := make([]string, 0, len(stats.PerRepo))
		for k := range stats.PerRepo {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, stats.PerRepo[k])
		}
	}
	if len(stats.PerScope) > 0 {
		fmt.Println("  per scope:")
		keys := make([]string, 0, len(stats.PerScope))
		for k := range stats.PerScope {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, stats.PerScope[k])
		}
	}
	return 0
}

func scanMode(fullScan bool) string {
	if fullScan {
		return "full-scan (ignoring manifest)"
	}
	return "incremental (manifest-aware)"
}
