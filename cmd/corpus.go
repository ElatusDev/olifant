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

// Corpus dispatches `olifant corpus <build|diff|index|scan|prose|classify|stats>`.
//
// build/diff/index belong to the v1 corpus pipeline (ChromaDB indexing,
// per platform/knowledge-base/corpus/CORPUS-V1.md). scan/prose/stats
// belong to the v2 curriculum extractor (per
// olifant-fine-tune-v2-corpus-curriculum-workflow.md) — same package
// because they share the corpus root abstraction but emit different
// downstream artefacts (Chunk JSONL vs Symbol/Sentence YAML).
func Corpus(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant corpus: missing action (build|diff|index|scan|prose|stats)")
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
	case "scan":
		return corpusScan(rest)
	case "prose":
		return corpusProse(rest)
	case "classify":
		return corpusClassify(rest)
	case "stats":
		return corpusStats(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant corpus: unknown action %q\n", action)
		return 2
	}
}

// corpusScan runs a v2-curriculum vocabulary extractor and writes a
// per-(repo, module) Symbol YAML to the KB. Per D-CC2, the active
// extractor is chosen by repo name. Day 1 implements Java only; the TS,
// HCL, JSON, and KB extractors land Days 3-4.
func corpusScan(args []string) int {
	fs := flag.NewFlagSet("corpus scan", flag.ExitOnError)
	repo := fs.String("repo", "", "repo name (core-api, akademia-plus-web, ...) (required)")
	repoRoot := fs.String("repo-root", "", "absolute path to repo root (default: autodetect via sibling-of-knowledge-base)")
	module := fs.String("module", "", "module/feature within the repo (required for core-api/infra)")
	sourceRoot := fs.String("source-root", "", "override the per-repo source-root convention")
	out := fs.String("out", "", "output YAML path (default: <kb-root>/corpus/v2-curriculum/vocab/<repo>/<module>.yaml)")
	dryRun := fs.Bool("dry-run", false, "extract + count only; no YAML write")
	verbose := fs.Bool("v", false, "verbose per-file progress")
	_ = fs.Parse(args)

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "corpus scan: --repo required")
		return 2
	}

	rr := *repoRoot
	if rr == "" {
		// findUp returns the absolute path to "knowledge-base/README.md"
		// (e.g., /…/platform/knowledge-base/README.md). The platform root
		// is the GRANDPARENT (two filepath.Dir hops up from the README).
		if found, ok := findUp("knowledge-base/README.md"); ok {
			platformRoot := filepath.Dir(filepath.Dir(found))
			rr = filepath.Join(platformRoot, *repo)
		}
	}
	if rr == "" {
		fmt.Fprintln(os.Stderr, "corpus scan: --repo-root not specified and not autodetected")
		return 1
	}
	rr, _ = filepath.Abs(rr)
	if _, err := os.Stat(rr); err != nil {
		fmt.Fprintf(os.Stderr, "corpus scan: repo-root %s: %v\n", rr, err)
		return 1
	}

	sr := *sourceRoot
	if sr == "" {
		switch *repo {
		case "core-api":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus scan: --module required for core-api")
				return 2
			}
			sr = filepath.Join(rr, *module, "src", "main", "java")
		case "akademia-plus-web", "elatusdev-web", "akademia-plus-central", "akademia-plus-go":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus scan: --module required for webapp/mobile repos (feature dir name under src/features/)")
				return 2
			}
			sr = filepath.Join(rr, "src", "features", *module)
		case "infra":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus scan: --module required for infra ('root' for terraform/*.tf, or a name under terraform/modules/)")
				return 2
			}
			if *module == "root" {
				sr = filepath.Join(rr, "terraform")
			} else {
				sr = filepath.Join(rr, "terraform", "modules", *module)
			}
		case "core-api-e2e":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus scan: --module required for core-api-e2e (collection name without .postman_collection.json suffix)")
				return 2
			}
			// Resolve to single collection file. Try the standard suffix
			// first; fall back to a bare .json (some collections use
			// non-standard names like platform-api-e2e.json).
			cand := filepath.Join(rr, "Postman Collections", *module+".postman_collection.json")
			if _, err := os.Stat(cand); err == nil {
				sr = cand
			} else {
				sr = filepath.Join(rr, "Postman Collections", *module+".json")
			}
		case "knowledge-base":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus scan: --module required for knowledge-base (one of: decisions, anti-patterns, patterns, dictionary)")
				return 2
			}
			sr = filepath.Join(rr, *module)
		default:
			fmt.Fprintf(os.Stderr, "corpus scan: source-root autodetection not yet implemented for repo %q (use --source-root)\n", *repo)
			return 1
		}
	}
	sr, _ = filepath.Abs(sr)
	if _, err := os.Stat(sr); err != nil {
		fmt.Fprintf(os.Stderr, "corpus scan: source-root %s: %v\n", sr, err)
		return 1
	}

	outPath := *out
	if outPath == "" && !*dryRun {
		kbReadme, _ := findUp("knowledge-base/README.md")
		if kbReadme == "" {
			fmt.Fprintln(os.Stderr, "corpus scan: --out required and KB not autodetected")
			return 2
		}
		// kbDir = .../platform/knowledge-base (the README's parent).
		kbDir := filepath.Dir(kbReadme)
		name := *module
		if name == "" {
			name = "default"
		}
		// Webapp/mobile vocab is grouped under a 'features/' subdir per
		// workflow §6 AC convention (vocab/<repo>/features/<f>.yaml).
		// core-api keeps the flat vocab/<repo>/<module>.yaml layout.
		var pathParts []string
		switch *repo {
		case "akademia-plus-web", "elatusdev-web", "akademia-plus-central", "akademia-plus-go":
			pathParts = []string{kbDir, "corpus", "v2-curriculum", "vocab", *repo, "features", name + ".yaml"}
		default:
			pathParts = []string{kbDir, "corpus", "v2-curriculum", "vocab", *repo, name + ".yaml"}
		}
		outPath = filepath.Join(pathParts...)
	}
	if outPath != "" {
		outPath, _ = filepath.Abs(outPath)
	}

	fmt.Println("repo:        ", *repo)
	fmt.Println("repo-root:   ", rr)
	fmt.Println("source-root: ", sr)
	fmt.Println("module:      ", *module)
	if *dryRun {
		fmt.Println("mode:         dry-run")
	} else {
		fmt.Println("out:         ", outPath)
	}

	stats, err := corpus.Scan(corpus.ScanConfig{
		Repo:       *repo,
		RepoRoot:   rr,
		Module:     *module,
		SourceRoot: sr,
		OutPath:    outPath,
		DryRun:     *dryRun,
		Verbose:    *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus scan:", err)
		return 1
	}

	fmt.Println("scan summary:")
	fmt.Printf("  files scanned:    %d\n", stats.FilesScanned)
	fmt.Printf("  symbols emitted:  %d\n", stats.SymbolsEmitted)
	fmt.Printf("  elapsed:          %s\n", stats.Elapsed.Round(time.Millisecond))

	if len(stats.ByKind) > 0 {
		fmt.Println("  by kind:")
		ks := make([]string, 0, len(stats.ByKind))
		for k := range stats.ByKind {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-14s %d\n", k, stats.ByKind[k])
		}
	}
	if len(stats.ByConcern) > 0 {
		fmt.Println("  by concern:")
		ks := make([]string, 0, len(stats.ByConcern))
		for k := range stats.ByConcern {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-14s %d\n", k, stats.ByConcern[k])
		}
	}
	return 0
}

// corpusProse is the Day-5 Phase-1 tokenizer + rule-based classifier.
// Walks .md files under cfg.SourceRoot, extracts sentences, populates
// syntactic_form + modality axes via keyword rules, writes a Sentence
// YAML list. The semantic_role + concern (LLM-classified) axes are
// filled by Phase-2 via claude-code (HARD RULE).
func corpusProse(args []string) int {
	fs := flag.NewFlagSet("corpus prose", flag.ExitOnError)
	repo := fs.String("repo", "", "repo name (one of the 7 platform repos) (required)")
	repoRoot := fs.String("repo-root", "", "absolute path to repo root (default: autodetect sibling-of-knowledge-base)")
	module := fs.String("module", "", "module/sub-dir slice. Required for knowledge-base; optional elsewhere (default: whole repo)")
	sourceRoot := fs.String("source-root", "", "override the source-root convention")
	out := fs.String("out", "", "output YAML path (default: <kb-root>/corpus/v2-curriculum/prose/<repo>/<module>.yaml)")
	dryRun := fs.Bool("dry-run", false, "tokenize + count only; no YAML write")
	verbose := fs.Bool("v", false, "verbose per-file progress")
	_ = fs.Parse(args)

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "corpus prose: --repo required")
		return 2
	}

	rr := *repoRoot
	if rr == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			platformRoot := filepath.Dir(filepath.Dir(found))
			rr = filepath.Join(platformRoot, *repo)
		}
	}
	if rr == "" {
		fmt.Fprintln(os.Stderr, "corpus prose: --repo-root not specified and not autodetected")
		return 1
	}
	rr, _ = filepath.Abs(rr)
	if _, err := os.Stat(rr); err != nil {
		fmt.Fprintf(os.Stderr, "corpus prose: repo-root %s: %v\n", rr, err)
		return 1
	}

	sr := *sourceRoot
	if sr == "" {
		switch *repo {
		case "knowledge-base":
			if *module == "" {
				fmt.Fprintln(os.Stderr, "corpus prose: --module required for knowledge-base (e.g. patterns, anti-patterns, decisions, standards, architecture, skills, templates, dictionary, runbooks)")
				return 2
			}
			sr = filepath.Join(rr, *module)
		default:
			// All other repos: walk the whole repo root for .md files.
			sr = rr
		}
	}
	sr, _ = filepath.Abs(sr)
	if _, err := os.Stat(sr); err != nil {
		fmt.Fprintf(os.Stderr, "corpus prose: source-root %s: %v\n", sr, err)
		return 1
	}

	outPath := *out
	if outPath == "" && !*dryRun {
		kbReadme, _ := findUp("knowledge-base/README.md")
		if kbReadme == "" {
			fmt.Fprintln(os.Stderr, "corpus prose: --out required and KB not autodetected")
			return 2
		}
		kbDir := filepath.Dir(kbReadme)
		name := *module
		if name == "" {
			name = *repo
		}
		// prose/<repo>/<slice>.yaml; for non-KB repos slice == repo
		// (collapsed to prose/<repo>.yaml).
		var pathParts []string
		if *repo == "knowledge-base" {
			pathParts = []string{kbDir, "corpus", "v2-curriculum", "prose", *repo, name + ".yaml"}
		} else {
			pathParts = []string{kbDir, "corpus", "v2-curriculum", "prose", *repo + ".yaml"}
		}
		outPath = filepath.Join(pathParts...)
	}
	if outPath != "" {
		outPath, _ = filepath.Abs(outPath)
	}

	fmt.Println("repo:        ", *repo)
	fmt.Println("repo-root:   ", rr)
	fmt.Println("source-root: ", sr)
	fmt.Println("module:      ", *module)
	if *dryRun {
		fmt.Println("mode:         dry-run")
	} else {
		fmt.Println("out:         ", outPath)
	}

	stats, err := corpus.Prose(corpus.ScanConfig{
		Repo:       *repo,
		RepoRoot:   rr,
		Module:     *module,
		SourceRoot: sr,
		OutPath:    outPath,
		DryRun:     *dryRun,
		Verbose:    *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus prose:", err)
		return 1
	}

	fmt.Println("prose summary:")
	fmt.Printf("  files scanned:    %d\n", stats.FilesScanned)
	fmt.Printf("  sentences:        %d\n", stats.SymbolsEmitted)
	fmt.Printf("  elapsed:          %s\n", stats.Elapsed.Round(time.Millisecond))

	if len(stats.ByKind) > 0 {
		fmt.Println("  axis distribution:")
		ks := make([]string, 0, len(stats.ByKind))
		for k := range stats.ByKind {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-22s %d\n", k, stats.ByKind[k])
		}
	}
	if len(stats.ByConcern) > 0 {
		fmt.Println("  by concern:")
		ks := make([]string, 0, len(stats.ByConcern))
		for k := range stats.ByConcern {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-22s %d\n", k, stats.ByConcern[k])
		}
	}
	return 0
}

func corpusStats(args []string) int {
	fmt.Fprintln(os.Stderr, "corpus stats: not yet implemented")
	return 1
}

// corpusClassify is the Day-5 Phase-2 LLM classifier. Shells out to
// `claude --print` (HARD RULE: no SDK/HTTP) per batch to populate
// semantic_role + concern axes on prose YAML sentences. Resumable:
// sentences already tagged are skipped.
func corpusClassify(args []string) int {
	fs := flag.NewFlagSet("corpus classify", flag.ExitOnError)
	input := fs.String("input", "", "path to a prose YAML, OR a dir containing prose YAMLs (required)")
	batchSize := fs.Int("batch", 100, "sentences per claude invocation (50-100 recommended)")
	model := fs.String("model", "haiku", "claude model alias or full name")
	verbose := fs.Bool("v", false, "verbose per-batch progress")
	dryRun := fs.Bool("dry-run", false, "show batch plan + count; skip subprocess + write")
	_ = fs.Parse(args)

	if *input == "" {
		fmt.Fprintln(os.Stderr, "corpus classify: --input required (file or dir)")
		return 2
	}

	// Expand --input: file → single-item slice; dir → walk for *.yaml.
	abs, _ := filepath.Abs(*input)
	info, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpus classify: stat %s: %v\n", abs, err)
		return 1
	}
	var files []string
	if info.IsDir() {
		err := filepath.WalkDir(abs, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".yaml") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "corpus classify: walk %s: %v\n", abs, err)
			return 1
		}
		sort.Strings(files)
	} else {
		files = []string{abs}
	}

	fmt.Printf("classify: %d file(s), batch=%d, model=%s, dry-run=%v\n",
		len(files), *batchSize, *model, *dryRun)

	var totalIn, totalOut, totalOK, totalFail int
	startAll := time.Now()
	for i, path := range files {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(files), path)
		stats, err := corpus.Classify(corpus.ClassifyConfig{
			InputPath: path,
			BatchSize: *batchSize,
			Model:     *model,
			Verbose:   *verbose,
			DryRun:    *dryRun,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", err)
			continue
		}
		fmt.Printf("  in=%d classified=%d batches ok/fail=%d/%d elapsed=%s\n",
			stats.InputSentences, stats.ClassifiedCount,
			stats.BatchesOK, stats.BatchesFailed, stats.Elapsed.Round(time.Second))
		totalIn += stats.InputSentences
		totalOut += stats.ClassifiedCount
		totalOK += stats.BatchesOK
		totalFail += stats.BatchesFailed
	}
	fmt.Printf("\nclassify totals: %d/%d sentences classified across %d ok batches (%d failed) in %s\n",
		totalOut, totalIn, totalOK, totalFail, time.Since(startAll).Round(time.Second))
	return 0
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
