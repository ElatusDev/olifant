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

// Corpus dispatches `olifant corpus <build|index|index-v2|scan|prose|classify|stats>`.
//
// build/index belong to the v1 corpus pipeline (ChromaDB indexing,
// per platform/knowledge-base/corpus/CORPUS-V1.md). scan/prose/stats
// belong to the v2 curriculum extractor (per
// olifant-fine-tune-v2-corpus-curriculum-workflow.md) — same package
// because they share the corpus root abstraction but emit different
// downstream artefacts (Chunk JSONL vs Symbol/Sentence YAML).
//
// index-v2 belongs to the RAG pivot (olifant-rag-pivot-workflow.md
// Phase A1): walks the v2-curriculum YAMLs and upserts into a fresh
// Chroma collection with tag-axis metadata preserved.
func Corpus(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant corpus: missing action (build|sync|status|index|index-v2|scan|prose|classify|stats)")
		return 2
	}
	action, rest := args[0], args[1:]

	switch action {
	case "build":
		return corpusBuild(rest)
	case "sync":
		return corpusSync(rest)
	case "status":
		return corpusStatus(rest)
	case "index":
		return corpusIndex(rest)
	case "index-v2":
		return corpusIndexV2(rest)
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

// corpusSync implements `olifant corpus sync` (olifant#77, D-CS2): the
// incremental re-index — manifest diff → delete-by-source → embed only
// added/changed. Honors -kb-root/OLIFANT_KB_ROOT (D-CS7) and passes the REAL
// platform root explicitly: a pinned worktree's parent is worktrees/, not the
// platform, and repo CLAUDE.md walking must not follow the pin.
func corpusSync(args []string) int {
	fs := flag.NewFlagSet("corpus sync", flag.ExitOnError)
	kbRootFlag := fs.String("kb-root", "", "KB tree to sync (default: OLIFANT_KB_ROOT, then findUp)")
	memoryRoot := fs.String("memory-root", "", "memory directory (default: $HOME/.claude/projects/.../memory)")
	batchSize := fs.Int("batch", 32, "chunks per embed/upsert batch")
	dryRun := fs.Bool("dry-run", false, "diff + report only; no deletes, embeds, or writes")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 1800, "overall timeout in seconds")
	_ = fs.Parse(args)

	kbRoot, platformRoot := resolveRoots(*kbRootFlag)
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "corpus sync: kb-root not found (run from the platform tree, or pass -kb-root)")
		return 2
	}
	cfg, err := corpus.ResolveConfig(corpus.Config{
		KBRoot: kbRoot, PlatformRoot: platformRoot, MemoryRoot: *memoryRoot, Verbose: *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus sync:", err)
		return 1
	}

	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	rep, err := corpus.Sync(ctx, corpus.SyncConfig{
		Config:    cfg,
		OllamaURL: rt.OllamaURL,
		ChromaURL: rt.ChromaURL,
		Embedder:  rt.Embedder,
		Tenant:    rt.ChromaTenant,
		Database:  rt.ChromaDatabase,
		BatchSize: *batchSize,
		DryRun:    *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus sync:", err)
		return 1
	}

	mode := "synced"
	switch {
	case rep.NoOp:
		mode = "no-op (index already current; nothing written)"
	case *dryRun:
		mode = "dry-run (nothing touched)"
	}
	fmt.Printf("corpus sync %s — added=%d changed=%d removed=%d chunks_embedded=%d elapsed=%s kb_root=%s\n",
		mode, rep.Added, rep.Changed, rep.Removed, rep.ChunksEmbedded,
		(time.Duration(rep.ElapsedMs) * time.Millisecond).Round(time.Millisecond), cfg.KBRoot)
	return 0
}

// corpusStatus implements `olifant corpus status` (olifant#77, D-CS6): the
// freshness observable — index age + source drift vs the KB tree. The diff
// half is offline; -chroma adds live per-scope collection counts.
func corpusStatus(args []string) int {
	fs := flag.NewFlagSet("corpus status", flag.ExitOnError)
	kbRootFlag := fs.String("kb-root", "", "KB tree to compare (default: OLIFANT_KB_ROOT, then findUp)")
	memoryRoot := fs.String("memory-root", "", "memory directory (default: $HOME/.claude/projects/.../memory)")
	withChroma := fs.Bool("chroma", false, "also report live per-scope collection counts")
	_ = fs.Parse(args)

	kbRoot, platformRoot := resolveRoots(*kbRootFlag)
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "corpus status: kb-root not found (run from the platform tree, or pass -kb-root)")
		return 2
	}
	cfg, err := corpus.ResolveConfig(corpus.Config{
		KBRoot: kbRoot, PlatformRoot: platformRoot, MemoryRoot: *memoryRoot,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus status:", err)
		return 1
	}

	rep, err := corpus.Status(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus status:", err)
		return 1
	}
	fmt.Printf("corpus status — kb_root=%s\n", cfg.KBRoot)
	fmt.Printf("  indexed:  built_at=%s builder=%s sources=%d chunks=%d\n",
		rep.BuiltAt, rep.BuilderVersion, rep.IndexedSources, rep.IndexedChunks)
	fmt.Printf("  drift:    added=%d changed=%d removed=%d (total %d)\n",
		rep.Added, rep.Changed, rep.Removed, rep.Added+rep.Changed+rep.Removed)
	if rep.VersionDrift {
		fmt.Println("  WARNING:  builder version drift — sync will refuse; full drop-and-rebuild required (AP179 recipe)")
	}
	if *withChroma {
		rt := config.Resolve()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		counts, cerr := corpus.LiveCounts(ctx, rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "  chroma:   unreachable (%v)\n", cerr)
		} else {
			for _, sc := range corpus.AllScopes {
				fmt.Printf("  chroma:   %-18s %d\n", sc, counts[sc])
			}
		}
	}
	if rep.Added+rep.Changed+rep.Removed > 0 {
		return 1 // drift present — scriptable signal (nightly's cheap guard)
	}
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

// corpusIndexV2 is the RAG-pivot Phase A1 indexer: walks
// <kb-root>/corpus/v2-curriculum/{vocab,prose}/**/*.yaml, embeds via
// nomic-embed-text, and upserts into a single Chroma collection
// (default: olifant-v2-curriculum) with tag-axis metadata preserved.
// The v1 indexer + corpus_<scope> collections are left untouched.
func corpusIndexV2(args []string) int {
	fs := flag.NewFlagSet("corpus index-v2", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect)")
	collection := fs.String("collection", corpus.DefaultV2Collection, "Chroma collection name")
	batchSize := fs.Int("batch", 32, "items per embed/upsert batch")
	onlyKinds := fs.String("only-kinds", "", "comma-separated subset of {vocab,prose} (default: both)")
	verbose := fs.Bool("v", false, "verbose progress")
	dryRun := fs.Bool("dry-run", false, "walk + count only; skip embed + upsert")
	smoke := fs.Bool("smoke", false, "after upsert, run 5 canned retrieval smoke queries")
	smokeOut := fs.String("smoke-out", "", "write smoke report markdown here (default: <kb-root>/short-term/recovery/<utc-ts>-a1.md when --smoke is set)")
	timeoutSec := fs.Int("timeout", 1800, "overall timeout in seconds")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		} else {
			fmt.Fprintln(os.Stderr, "corpus index-v2: --kb-root not specified and knowledge-base not found")
			return 1
		}
	}
	root, _ = filepath.Abs(root)

	var only []string
	if *onlyKinds != "" {
		for _, s := range strings.Split(*onlyKinds, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				only = append(only, s)
			}
		}
	}

	out := *smokeOut
	if *smoke && out == "" {
		ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
		out = filepath.Join(root, "short-term", "recovery", ts+"-a1.md")
	}

	rt := config.Resolve()
	fmt.Println("config:", rt.String())
	fmt.Println("kb-root:    ", root)
	fmt.Println("collection: ", *collection)
	if len(only) > 0 {
		fmt.Println("only-kinds: ", strings.Join(only, ","))
	}
	if *dryRun {
		fmt.Println("mode:        dry-run")
	}
	if *smoke {
		fmt.Println("smoke-out:  ", out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	stats, err := corpus.IndexV2(ctx, corpus.IndexV2Config{
		KBRoot:     root,
		Collection: *collection,
		OllamaURL:  rt.OllamaURL,
		ChromaURL:  rt.ChromaURL,
		Embedder:   rt.Embedder,
		Tenant:     rt.ChromaTenant,
		Database:   rt.ChromaDatabase,
		BatchSize:  *batchSize,
		OnlyKinds:  only,
		Verbose:    *verbose,
		DryRun:     *dryRun,
		Smoke:      *smoke,
		SmokeOut:   out,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus index-v2:", err)
		return 1
	}

	fmt.Println("index-v2 summary:")
	fmt.Printf("  vocab files:        %d\n", stats.VocabFilesRead)
	fmt.Printf("  prose files:        %d\n", stats.ProseFilesRead)
	fmt.Printf("  symbols read:       %d\n", stats.SymbolsRead)
	fmt.Printf("  sentences read:     %d\n", stats.SentencesRead)
	fmt.Printf("  chunks upserted:    %d\n", stats.ChunksUpserted)
	fmt.Printf("  batches sent:       %d\n", stats.BatchesSent)
	fmt.Printf("  elapsed:            %s\n", stats.Elapsed.Round(time.Millisecond))
	if len(stats.ByKind) > 0 {
		fmt.Println("  by item kind:")
		ks := make([]string, 0, len(stats.ByKind))
		for k := range stats.ByKind {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-12s %d\n", k, stats.ByKind[k])
		}
	}
	if len(stats.ByRepo) > 0 {
		fmt.Println("  by repo:")
		ks := make([]string, 0, len(stats.ByRepo))
		for k := range stats.ByRepo {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("    %-24s %d\n", k, stats.ByRepo[k])
		}
	}
	if len(stats.Smoke) > 0 {
		fmt.Println("  smoke queries:")
		for i, r := range stats.Smoke {
			fmt.Printf("    Q%d: %s  (%d hits)\n", i+1, r.Query, len(r.Hits))
		}
		if out != "" {
			fmt.Println("  smoke report:    ", out)
		}
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
