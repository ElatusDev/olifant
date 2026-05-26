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

	"github.com/ElatusDev/olifant/dataset"
	"github.com/ElatusDev/olifant/internal/embedder"
	"github.com/ElatusDev/olifant/internal/format"
)

// Dataset dispatches `olifant dataset <build|stats|index|pack|sanitize-docs|format-pairs>`
// per the olifant-training-plan.md §4 extraction recipe (build/stats), the
// failure-modes ChromaDB indexer (index), the LoRA-upload packer (pack),
// the markdown attribution sweeper (sanitize-docs), and the RAG-pivot
// Phase C1 verdict-YAML training-pair pipeline (format-pairs).
func Dataset(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant dataset: missing action (build|stats|index|pack|sanitize-docs|format-pairs|embedder-triples)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "build":
		return datasetBuild(rest)
	case "stats":
		return datasetStats(rest)
	case "index":
		return datasetIndex(rest)
	case "pack":
		return datasetPack(rest)
	case "sanitize-docs":
		return datasetSanitizeDocs(rest)
	case "format-pairs":
		return datasetFormatPairs(rest)
	case "embedder-triples":
		return datasetEmbedderTriples(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant dataset: unknown action %q\n", action)
		return 2
	}
}

func datasetBuild(args []string) int {
	fs := flag.NewFlagSet("dataset build", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect via cwd ancestors)")
	outDir := fs.String("out", "", "output dir (default: <kb-root>/training/<YYYY-MM-DD>)")
	sourcesFlag := fs.String("sources", "all", "comma list of: retros,decisions,antipatterns,patterns,triples,failure-modes,all")
	dryRun := fs.Bool("dry-run", false, "extract + count only; no JSONL or manifest write")
	verbose := fs.Bool("v", false, "verbose per-source progress")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		}
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "dataset build: --kb-root not specified and not autodetected")
		return 1
	}
	root, _ = filepath.Abs(root)

	out := *outDir
	if out == "" {
		out = filepath.Join(root, "training", time.Now().UTC().Format("2006-01-02"))
	}
	out, _ = filepath.Abs(out)

	sources, err := parseSources(*sourcesFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset build:", err)
		return 2
	}

	fmt.Println("kb-root: ", root)
	fmt.Println("out:     ", out)
	fmt.Println("sources: ", strings.Join(sourcesToStrings(sources), ", "))
	if *dryRun {
		fmt.Println("mode:     dry-run (no writes)")
	}

	stats, err := dataset.Build(dataset.BuildConfig{
		KBRoot:     root,
		OutDir:     out,
		Sources:    sources,
		WriteJSONL: !*dryRun,
		Verbose:    *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset build:", err)
		return 1
	}

	fmt.Println("dataset build summary:")
	fmt.Printf("  sources processed: %d\n", stats.SourcesProcessed)
	fmt.Printf("  examples emitted:  %d\n", stats.ExamplesEmitted)
	fmt.Printf("  elapsed:           %s\n", stats.Elapsed.Round(time.Millisecond))
	fmt.Println("  per source:")

	names := make([]string, 0, len(stats.PerSource))
	for k := range stats.PerSource {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		s := stats.PerSource[n]
		fmt.Printf("    %-14s files=%-5d entries=%-5d examples=%d\n",
			n, s.FilesScanned, s.EntriesParsed, s.ExamplesEmitted)
	}

	if !*dryRun {
		fmt.Printf("  manifest:          %s\n", filepath.Join(out, "manifest.yaml"))
	}
	return 0
}

// datasetStats prints the manifest from a prior run. Kept simple —
// the manifest is YAML so users can also cat it directly.
func datasetStats(args []string) int {
	fs := flag.NewFlagSet("dataset stats", flag.ExitOnError)
	in := fs.String("out", "", "dataset output dir containing manifest.yaml (required)")
	_ = fs.Parse(args)
	if *in == "" {
		fmt.Fprintln(os.Stderr, "dataset stats: --out required")
		return 2
	}
	mpath := filepath.Join(*in, "manifest.yaml")
	data, err := os.ReadFile(mpath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset stats:", err)
		return 1
	}
	fmt.Print(string(data))
	return 0
}

// datasetIndex pushes failure-mode corrections into ChromaDB so the
// challenge runner can retrieve them at inference time. Idempotent —
// re-running against the same source is a no-op upsert.
func datasetIndex(args []string) int {
	fs := flag.NewFlagSet("dataset index", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect)")
	ollamaURL := fs.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	chromaURL := fs.String("chroma-url", "http://localhost:8000", "ChromaDB base URL (typically port-forwarded)")
	chromaTenant := fs.String("chroma-tenant", "default_tenant", "ChromaDB tenant")
	chromaDB := fs.String("chroma-database", "default_database", "ChromaDB database")
	embedder := fs.String("embedder", "nomic-embed-text", "Ollama embedding model")
	batchSize := fs.Int("batch", 32, "chunks per embed batch")
	dryRun := fs.Bool("dry-run", false, "load + chunk only; no embed, no upsert")
	verbose := fs.Bool("v", false, "verbose per-collection progress")
	timeoutSec := fs.Int("timeout", 600, "overall timeout in seconds")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		}
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "dataset index: --kb-root not specified and not autodetected")
		return 1
	}
	root, _ = filepath.Abs(root)

	fmt.Println("kb-root:    ", root)
	fmt.Println("ollama-url: ", *ollamaURL)
	fmt.Println("chroma-url: ", *chromaURL)
	fmt.Println("embedder:   ", *embedder)
	if *dryRun {
		fmt.Println("mode:        dry-run (no embed, no upsert)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	stats, err := dataset.IndexFailureModes(ctx, dataset.IndexConfig{
		KBRoot:       root,
		OllamaURL:    *ollamaURL,
		ChromaURL:    *chromaURL,
		ChromaTenant: *chromaTenant,
		ChromaDB:     *chromaDB,
		Embedder:     *embedder,
		BatchSize:    *batchSize,
		Verbose:      *verbose,
		DryRun:       *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset index:", err)
		return 1
	}

	fmt.Println("dataset index summary:")
	fmt.Printf("  entries read:    %d\n", stats.EntriesRead)
	fmt.Printf("  chunks built:    %d\n", stats.Chunks)
	fmt.Printf("  chunks upserted: %d\n", stats.Upserted)
	fmt.Printf("  batches sent:    %d\n", stats.BatchesSent)
	fmt.Printf("  elapsed:         %s\n", stats.Elapsed.Round(time.Millisecond))
	if len(stats.PerCollection) > 0 {
		fmt.Println("  per collection:")
		names := make([]string, 0, len(stats.PerCollection))
		for n := range stats.PerCollection {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("    %-32s %d chunks\n", n, stats.PerCollection[n])
		}
	}
	return 0
}

// datasetPack concatenates a dataset-build output dir's per-tier JSONLs
// into one ShareGPT JSONL ready for LoRA upload, stripping
// `Co-authored-by:…@nordstrom.com` lines from string fields per Hard
// Rule #4 of olifant-fine-tune-v1-prompt.md.
func datasetPack(args []string) int {
	fs := flag.NewFlagSet("dataset pack", flag.ExitOnError)
	inDir := fs.String("in", "", "training input dir (e.g. <kb-root>/training/2026-05-18) (required)")
	outPath := fs.String("out", "", "concatenated JSONL output path (required)")
	subdirsFlag := fs.String("subdirs", "", "comma list of top-level dirs to include; empty = all")
	verbose := fs.Bool("v", false, "verbose per-file progress")
	_ = fs.Parse(args)

	if *inDir == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "dataset pack: --in and --out required")
		return 2
	}
	inAbs, _ := filepath.Abs(*inDir)
	outAbs, _ := filepath.Abs(*outPath)

	var subdirs []string
	if s := strings.TrimSpace(*subdirsFlag); s != "" {
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				subdirs = append(subdirs, t)
			}
		}
	}

	fmt.Println("in:   ", inAbs)
	fmt.Println("out:  ", outAbs)
	if len(subdirs) > 0 {
		fmt.Println("subdirs:", strings.Join(subdirs, ","))
	}

	stats, err := dataset.Pack(dataset.PackConfig{
		InputDir: inAbs,
		OutPath:  outAbs,
		Subdirs:  subdirs,
		Verbose:  *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset pack:", err)
		return 1
	}

	fmt.Println("dataset pack summary:")
	fmt.Printf("  files scanned:            %d\n", stats.FilesScanned)
	fmt.Printf("  lines in:                 %d\n", stats.LinesIn)
	fmt.Printf("  lines out:                %d\n", stats.LinesOut)
	fmt.Printf("  lines modified:           %d\n", stats.LinesModified)
	fmt.Printf("  nordstrom lines stripped: %d\n", stats.NordstromLinesStripped)
	fmt.Printf("  bytes out:                %d\n", stats.BytesOut)
	fmt.Printf("  elapsed:                  %s\n", stats.Elapsed.Round(time.Millisecond))
	return 0
}

// datasetSanitizeDocs walks a directory tree and strips
// Claude/Anthropic + nordstrom-email attribution lines from every *.md
// file. Functional references (CLAUDE.md filename, claude-code CLI,
// com.anthropic Maven group, prose mentions) are preserved.
func datasetSanitizeDocs(args []string) int {
	fs := flag.NewFlagSet("dataset sanitize-docs", flag.ExitOnError)
	root := fs.String("root", "", "directory to walk recursively (required)")
	dryRun := fs.Bool("dry-run", false, "print what would change; do not write")
	verbose := fs.Bool("v", false, "print per-file modifications")
	_ = fs.Parse(args)

	if *root == "" {
		fmt.Fprintln(os.Stderr, "dataset sanitize-docs: --root required")
		return 2
	}
	rootAbs, _ := filepath.Abs(*root)
	fmt.Println("root:   ", rootAbs)
	if *dryRun {
		fmt.Println("mode:    dry-run (no writes)")
	}

	stats, err := dataset.SanitizeDocs(dataset.SanitizeDocsConfig{
		Root:    rootAbs,
		DryRun:  *dryRun,
		Verbose: *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset sanitize-docs:", err)
		return 1
	}

	fmt.Println("sanitize-docs summary:")
	fmt.Printf("  files scanned:    %d\n", stats.FilesScanned)
	fmt.Printf("  files modified:   %d\n", stats.FilesModified)
	fmt.Printf("  lines stripped:   %d\n", stats.LinesStripped)
	fmt.Printf("  bytes before:     %d\n", stats.BytesBefore)
	fmt.Printf("  bytes after:      %d\n", stats.BytesAfter)
	fmt.Printf("  elapsed:          %s\n", stats.Elapsed.Round(time.Millisecond))
	return 0
}

// parseSources converts the --sources flag string into a slice. "all"
// is an alias for the full canonical set.
func parseSources(s string) ([]dataset.SourceKind, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return append([]dataset.SourceKind(nil), dataset.AllSources...), nil
	}
	known := map[string]dataset.SourceKind{}
	for _, k := range dataset.AllSources {
		known[string(k)] = k
	}
	var out []dataset.SourceKind
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		k, ok := known[tok]
		if !ok {
			return nil, fmt.Errorf("unknown source %q (valid: retros,decisions,antipatterns,patterns,triples,failure-modes)", tok)
		}
		out = append(out, k)
	}
	return out, nil
}

func sourcesToStrings(xs []dataset.SourceKind) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// datasetFormatPairs runs the RAG-pivot Phase C1 verdict-YAML pair pipeline.
// 2 stages, both via `claude --print --model opus` subprocess per
// feedback_olifant_uses_claude_code_only.md + _opus_latest.md:
//
//	stage 1 (paraphrase): archetype seed → N paraphrastic variants
//	stage 2 (synth):      each variant → 1 verdict-YAML doc
//
// Output: append-only JSONL at --out (default
// ~/.olifant/training/format-v1/pairs.jsonl). Resumable: on --resume,
// prompts already present in the file are skipped.
func datasetFormatPairs(args []string) int {
	fs := flag.NewFlagSet("dataset format-pairs", flag.ExitOnError)
	out := fs.String("out", "", "JSONL output path (default: ~/.olifant/training/format-v1/pairs.jsonl)")
	model := fs.String("model", "opus", "claude model (must remain opus per HARD RULE)")
	bin := fs.String("claude-bin", "claude", "claude CLI binary")
	variants := fs.Int("variants", 30, "paraphrastic variants per archetype")
	conc := fs.Int("concurrency", 1, "parallel stage-2 calls (rate-limit-aware)")
	resume := fs.Bool("resume", true, "skip prompts already on disk")
	verbose := fs.Bool("v", false, "verbose progress logging")
	timeoutSec := fs.Int("per-call-timeout", 90, "per-claude-call timeout seconds")
	limit := fs.Int("limit", 0, "process only the first N archetypes (default 0 = all 50)")
	onlyIDs := fs.String("only", "", "comma-separated archetype IDs to include (overrides --limit)")
	overallTimeout := fs.Int("timeout", 0, "overall timeout in seconds (default 0 = none)")
	_ = fs.Parse(args)

	all := format.Archetypes()
	selected := all
	if *onlyIDs != "" {
		want := map[string]bool{}
		for _, s := range strings.Split(*onlyIDs, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				want[s] = true
			}
		}
		selected = selected[:0]
		for _, a := range all {
			if want[a.ID] {
				selected = append(selected, a)
			}
		}
		if len(selected) == 0 {
			fmt.Fprintf(os.Stderr, "dataset format-pairs: no archetypes matched --only=%s\n", *onlyIDs)
			return 2
		}
	} else if *limit > 0 && *limit < len(all) {
		selected = all[:*limit]
	}

	fmt.Println("format-pairs config:")
	fmt.Printf("  archetypes:       %d (of %d)\n", len(selected), len(all))
	fmt.Printf("  variants/arch:    %d\n", *variants)
	fmt.Printf("  concurrency:      %d\n", *conc)
	fmt.Printf("  per-call timeout: %ds\n", *timeoutSec)
	fmt.Printf("  resume:           %v\n", *resume)
	fmt.Printf("  model:            %s\n", *model)
	if *out != "" {
		fmt.Printf("  out:              %s\n", *out)
	}

	ctx := context.Background()
	if *overallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*overallTimeout)*time.Second)
		defer cancel()
	}

	stats, err := format.Generate(ctx, format.GenConfig{
		Archetypes:      selected,
		VariantsPerArch: *variants,
		OutPath:         *out,
		ClaudeBin:       *bin,
		Model:           *model,
		Resume:          *resume,
		Concurrency:     *conc,
		MaxRetries:      1,
		Verbose:         *verbose,
		PerCallTimeout:  time.Duration(*timeoutSec) * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataset format-pairs:", err)
		return 1
	}

	fmt.Println()
	fmt.Println("format-pairs summary:")
	fmt.Printf("  archetypes processed: %d / %d\n", stats.ArchetypesProcessed, len(selected))
	fmt.Printf("  stage 1 calls:        %d (failures %d, elapsed %s)\n",
		stats.StageOneCalls, stats.StageOneFailures, stats.StageOneElapsed.Round(time.Second))
	fmt.Printf("  stage 2 variants:     attempted %d  accepted %d  rejected %d\n",
		stats.VariantsAttempted, stats.VariantsAccepted, stats.VariantsRejected)
	fmt.Printf("  stage 2 elapsed:      %s\n", stats.StageTwoElapsed.Round(time.Second))
	fmt.Printf("  total elapsed:        %s\n", stats.TotalElapsed.Round(time.Second))
	return 0
}

// datasetEmbedderTriples runs the RAG-pivot Phase B1a pipeline: load the
// Day-5 v2-curriculum prose corpus, corpus-mine one hard negative per anchor
// (same-scope+different-role, cosine-similar over bag-of-tags), and call
// Opus for one paraphrastic positive per anchor. Writes append-only JSONL.
//
// Reference: knowledge-base/architecture/olifant-rag-phase-b-prompt.md §4 B1a.
// HARD RULE: all LLM calls route through `claude --print --model opus`.
func datasetEmbedderTriples(args []string) int {
	fs := flag.NewFlagSet("dataset embedder-triples", flag.ExitOnError)
	proseDir := fs.String("prose-dir", "", "v2-curriculum prose dir (default: <kb-root>/corpus/v2-curriculum/prose)")
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect via cwd ancestors)")
	out := fs.String("out", "", "JSONL output path (default: ~/.olifant/training/embedder-v1/triples.jsonl)")
	model := fs.String("model", "opus", "claude model (must remain opus per HARD RULE)")
	bin := fs.String("claude-bin", "claude", "claude CLI binary")
	limit := fs.Int("limit", 0, "process only first N anchors (0 = all; 1000 for §4 B1a sanity gate)")
	conc := fs.Int("concurrency", 1, "parallel paraphrase calls (workflow default 1)")
	resume := fs.Bool("resume", true, "skip anchors already on disk by anchor_id")
	verbose := fs.Bool("v", false, "verbose per-anchor progress")
	timeoutSec := fs.Int("per-call-timeout", 60, "per-claude-call timeout seconds")
	overallTimeout := fs.Int("timeout", 0, "overall timeout in seconds (default 0 = none)")
	miningOnly := fs.Bool("mining-only", false, "load + mine + print stats; skip Opus call (sanity scaffold)")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		}
	}
	prose := *proseDir
	if prose == "" {
		if root == "" {
			fmt.Fprintln(os.Stderr, "dataset embedder-triples: --prose-dir or --kb-root required")
			return 1
		}
		prose = filepath.Join(root, "corpus", "v2-curriculum", "prose")
	}

	t0 := time.Now()
	sentences, err := embedder.LoadProse(prose)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load prose:", err)
		return 1
	}
	fmt.Printf("loaded %d sentences from %s in %s\n",
		len(sentences), prose, time.Since(t0).Round(time.Millisecond))

	t1 := time.Now()
	triples := embedder.Mine(sentences)
	mineElapsed := time.Since(t1)
	miningSt := embedder.Summarise(sentences, triples)
	fmt.Printf("mined %d triples (%s):\n", len(triples), mineElapsed.Round(time.Millisecond))
	fmt.Print(miningSt.HumanString())

	if *miningOnly {
		fmt.Println("(--mining-only: skipping Opus paraphrase pass)")
		return 0
	}

	ctx := context.Background()
	if *overallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*overallTimeout)*time.Second)
		defer cancel()
	}

	fmt.Println("embedder-triples config:")
	fmt.Printf("  limit:            %d (0 = all)\n", *limit)
	fmt.Printf("  concurrency:      %d\n", *conc)
	fmt.Printf("  per-call timeout: %ds\n", *timeoutSec)
	fmt.Printf("  resume:           %v\n", *resume)
	fmt.Printf("  model:            %s\n", *model)
	if *out != "" {
		fmt.Printf("  out:              %s\n", *out)
	}

	stats, err := embedder.Generate(ctx, embedder.GenConfig{
		Triples:        triples,
		OutPath:        *out,
		ClaudeBin:      *bin,
		Model:          *model,
		Resume:         *resume,
		Limit:          *limit,
		Concurrency:    *conc,
		MaxRetries:     1,
		PerCallTimeout: time.Duration(*timeoutSec) * time.Second,
		Verbose:        *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		return 1
	}

	fmt.Println()
	fmt.Println("embedder-triples summary:")
	fmt.Printf("  anchors in queue:     %d\n", stats.Anchors)
	fmt.Printf("  processed:            %d\n", stats.Processed)
	fmt.Printf("  succeeded:            %d (retried-once: %d)\n", stats.Succeeded, stats.RetriedOnce)
	fmt.Printf("  failed:               %d\n", stats.Failed)
	fmt.Printf("  resume-skipped:       %d\n", stats.Skipped)
	if stats.Processed > 0 {
		parseRate := float64(stats.Succeeded) / float64(stats.Processed) * 100
		fmt.Printf("  parse-success rate:   %.1f%% (workflow B1a sanity gate: ≥95%%)\n", parseRate)
	}
	fmt.Printf("  mean para/anchor len: %.2f\n", stats.MeanRatio)
	if stats.ArtifactIDTotal > 0 {
		idRate := float64(stats.ArtifactIDHits) / float64(stats.ArtifactIDTotal) * 100
		fmt.Printf("  artifact-ID retained: %d / %d (%.0f%% of anchors that had any)\n",
			stats.ArtifactIDHits, stats.ArtifactIDTotal, idRate)
	}
	fmt.Printf("  elapsed:              %s\n", stats.Elapsed.Round(time.Second))
	return 0
}
