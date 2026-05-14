// Package cmd wires subcommands to the internal/* implementations.
package cmd

import (
	"flag"
	"fmt"
	"os"

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
		fmt.Fprintln(os.Stderr, "corpus index: not yet implemented (waiting on ChromaDB)")
		return 1
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
