// Package cmd wires subcommands to the internal/* implementations.
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
	"github.com/ElatusDev/olifant/internal/corpus"
)

// Corpus dispatches `olifant corpus <build|diff|index>`.
func Corpus(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant corpus: missing action (build|diff|index)")
		return 2
	}
	action, rest := args[0], args[1:]

	switch action {
	case "build":
		return corpusBuild(rest)
	case "diff":
		fmt.Fprintln(os.Stderr, "corpus diff: not yet implemented")
		return 1
	case "index":
		return corpusIndex(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant corpus: unknown action %q\n", action)
		return 2
	}
}

func corpusBuild(args []string) int {
	fs := flag.NewFlagSet("corpus build", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: ../knowledge-base from binary location)")
	platformRoot := fs.String("platform-root", "", "platform root containing repo dirs with CLAUDE.md (default: ../)")
	memoryRoot := fs.String("memory-root", "", "memory directory (default: $HOME/.claude/projects/.../memory)")
	out := fs.String("out", "", "output directory (default: <kb-root>/corpus/v1)")
	verbose := fs.Bool("v", false, "verbose progress logging")
	_ = fs.Parse(args)

	cfg, err := corpus.ResolveConfig(corpus.Config{
		KBRoot:       *kbRoot,
		PlatformRoot: *platformRoot,
		MemoryRoot:   *memoryRoot,
		OutDir:       *out,
		Verbose:      *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus build:", err)
		return 1
	}

	if err := corpus.Build(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "corpus build:", err)
		return 1
	}
	return 0
}

func corpusIndex(args []string) int {
	fs := flag.NewFlagSet("corpus index", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect)")
	corpusDir := fs.String("corpus-dir", "", "corpus output directory (default: <kb-root>/corpus/v1)")
	batchSize := fs.Int("batch", 32, "chunks per embed/upsert batch")
	scopes := fs.String("scopes", "", "comma-separated scope filter (default: all .ndjson files in corpus dir)")
	verbose := fs.Bool("v", false, "verbose progress")
	dryRun := fs.Bool("dry-run", false, "skip embed + upsert; only walk and report")
	timeoutSec := fs.Int("timeout", 1800, "overall timeout in seconds")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		} else {
			fmt.Fprintln(os.Stderr, "corpus index: --kb-root not specified and knowledge-base not found")
			return 1
		}
	}
	root, _ = filepath.Abs(root)
	cd := *corpusDir
	if cd == "" {
		cd = filepath.Join(root, "corpus", "v1")
	}

	var onlyScopes []string
	if *scopes != "" {
		for _, s := range strings.Split(*scopes, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				onlyScopes = append(onlyScopes, s)
			}
		}
	}

	rt := config.Resolve()
	fmt.Println("config:", rt.String())

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	stats, err := corpus.Index(ctx, corpus.IndexConfig{
		CorpusDir:  cd,
		OllamaURL:  rt.OllamaURL,
		ChromaURL:  rt.ChromaURL,
		Embedder:   rt.Embedder,
		Tenant:     rt.ChromaTenant,
		Database:   rt.ChromaDatabase,
		BatchSize:  *batchSize,
		Verbose:    *verbose,
		DryRun:     *dryRun,
		OnlyScopes: onlyScopes,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus index:", err)
		return 1
	}

	fmt.Println("corpus index summary:")
	fmt.Printf("  scopes processed:  %d\n", stats.ScopesProcessed)
	fmt.Printf("  chunks read:       %d\n", stats.ChunksRead)
	fmt.Printf("  chunks upserted:   %d\n", stats.ChunksUpserted)
	fmt.Printf("  batches sent:      %d\n", stats.BatchesSent)
	fmt.Printf("  elapsed:           %s\n", stats.Elapsed.Round(time.Millisecond))
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
