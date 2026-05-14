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

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/repos"
)

// Repo dispatches `olifant repo <ingest|...>`.
func Repo(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant repo: missing action (ingest)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "ingest":
		return repoIngest(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant repo: unknown action %q\n", action)
		return 2
	}
}

func repoIngest(args []string) int {
	fs := flag.NewFlagSet("repo ingest", flag.ExitOnError)
	platformRoot := fs.String("platform-root", "", "platform root containing the 7 repos (default: parent of kb-root)")
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect; only used to derive --out)")
	out := fs.String("out", "", "code NDJSON output dir (default: <kb-root>/corpus/v1/code)")
	repoFilter := fs.String("repo", "", "comma-separated repo names to ingest (default: all 7)")
	batch := fs.Int("batch", 32, "chunks per embed/upsert batch")
	noWrite := fs.Bool("no-write", false, "skip NDJSON output, embed+upsert only")
	dryRun := fs.Bool("dry-run", false, "walk + chunk only; no embed, no upsert, no write")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 5400, "overall timeout in seconds (default: 90 min)")
	_ = fs.Parse(args)

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
		fmt.Fprintln(os.Stderr, "repo ingest: --platform-root not specified and kb-root not found")
		return 1
	}
	pr, _ = filepath.Abs(pr)

	outDir := *out
	if outDir == "" && root != "" {
		outDir = filepath.Join(root, "corpus", "v1", "code")
	}

	// Build repo list — defaults to all 7, filtered if --repo supplied.
	allRepos := repos.DefaultRepos(pr)
	var selected []repos.RepoSpec
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
		fmt.Fprintln(os.Stderr, "repo ingest: no repos selected")
		return 1
	}

	rt := config.Resolve()
	fmt.Println("config:", rt.String())
	fmt.Println("platform-root:", pr)
	if outDir != "" {
		fmt.Println("out:          ", outDir)
	}
	fmt.Println("repos:")
	for _, r := range selected {
		fmt.Printf("  - %-22s [%s]  %s\n", r.Name, r.Scope, r.Path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	stats, err := repos.Ingest(ctx, repos.IngestConfig{
		Repos:       selected,
		OutDir:      outDir,
		WriteNDJSON: !*noWrite,
		OllamaURL:   rt.OllamaURL,
		ChromaURL:   rt.ChromaURL,
		Embedder:    rt.Embedder,
		Tenant:      rt.ChromaTenant,
		Database:    rt.ChromaDatabase,
		BatchSize:   *batch,
		Verbose:     *verbose,
		DryRun:      *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "repo ingest:", err)
		return 1
	}

	fmt.Println("repo ingest summary:")
	fmt.Printf("  repos processed:    %d\n", stats.ReposProcessed)
	fmt.Printf("  files read:         %d\n", stats.FilesRead)
	fmt.Printf("  files skipped:      %d (empty after chunking)\n", stats.FilesSkipped)
	fmt.Printf("  chunks produced:    %d\n", stats.ChunksProduced)
	if !*dryRun {
		fmt.Printf("  chunks upserted:    %d\n", stats.ChunksUpserted)
		fmt.Printf("  batches sent:       %d\n", stats.BatchesSent)
	}
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
