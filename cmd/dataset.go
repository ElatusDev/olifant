package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/dataset"
)

// Dataset dispatches `olifant dataset <build|stats>` per the
// olifant-training-plan.md §4 extraction recipe.
func Dataset(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant dataset: missing action (build|stats)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "build":
		return datasetBuild(rest)
	case "stats":
		return datasetStats(rest)
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
