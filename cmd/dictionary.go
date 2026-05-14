package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ElatusDev/olifant/internal/dictionary"
)

// Dictionary dispatches `olifant dictionary <bootstrap|list|...>`.
func Dictionary(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant dictionary: missing action (bootstrap|list)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "bootstrap":
		return dictBootstrap(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant dictionary: unknown action %q\n", action)
		return 2
	}
}

func dictBootstrap(args []string) int {
	fs := flag.NewFlagSet("dictionary bootstrap", flag.ExitOnError)
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect)")
	corpusDir := fs.String("corpus-dir", "", "corpus output directory (default: <kb-root>/corpus/v1)")
	dictDir := fs.String("dictionary-dir", "", "dictionary directory (default: <kb-root>/dictionary)")
	verbose := fs.Bool("v", false, "verbose progress")
	dryRun := fs.Bool("dry-run", false, "show what would be written without changing files")
	_ = fs.Parse(args)

	root := *kbRoot
	if root == "" {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			root = filepath.Dir(found)
		} else {
			fmt.Fprintln(os.Stderr, "dictionary bootstrap: --kb-root not specified and knowledge-base not found")
			return 1
		}
	}
	root, _ = filepath.Abs(root)

	cd := *corpusDir
	if cd == "" {
		cd = filepath.Join(root, "corpus", "v1")
	}
	dd := *dictDir
	if dd == "" {
		dd = filepath.Join(root, "dictionary")
	}

	stats, err := dictionary.Bootstrap(dictionary.BootstrapConfig{
		CorpusDir:     cd,
		DictionaryDir: dd,
		Verbose:       *verbose,
		DryRun:        *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dictionary bootstrap:", err)
		return 1
	}

	fmt.Println("dictionary bootstrap summary:")
	fmt.Printf("  chunks read:           %d\n", stats.ChunksRead)
	fmt.Printf("  with artifact_id:      %d\n", stats.WithArtifactID)
	fmt.Printf("  unique artifact_id:    %d\n", stats.UniqueArtifactID)
	fmt.Printf("  entries added:         %d\n", stats.EntriesAdded)
	fmt.Printf("  entries skipped:       %d (already present)\n", stats.EntriesSkipped)
	if len(stats.PerScopeAdded) > 0 {
		fmt.Println("  per scope:")
		keys := make([]string, 0, len(stats.PerScopeAdded))
		for k := range stats.PerScopeAdded {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, stats.PerScopeAdded[k])
		}
	}
	if len(stats.PerCategoryAdded) > 0 {
		fmt.Println("  per category:")
		keys := make([]string, 0, len(stats.PerCategoryAdded))
		for k := range stats.PerCategoryAdded {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %-44s %d\n", k, stats.PerCategoryAdded[k])
		}
	}
	return 0
}

// findUp is duplicated from internal/corpus to keep cmd/ packages thin and
// independent. Walks up from cwd looking for `suffix`.
func findUp(suffix string) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(cwd, suffix)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", false
		}
		cwd = parent
	}
}
