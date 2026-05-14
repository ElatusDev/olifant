// Olifant — prompt-compaction + retrieval + validator layer for Claude Code
// over the ElatusDev/AkademiaPlus platform knowledge base.
//
// v1 entry point: subcommand dispatch only. Each subcommand lives in cmd/.
package main

import (
	"fmt"
	"os"

	"github.com/ElatusDev/olifant/cmd"
)

const version = "v0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "corpus":
		os.Exit(cmd.Corpus(args))
	case "dictionary", "dict":
		os.Exit(cmd.Dictionary(args))
	case "version", "--version", "-v":
		fmt.Println("olifant", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "olifant: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `olifant — local model orchestrator for ElatusDev/AkademiaPlus

USAGE:
  olifant <subcommand> [args]

SUBCOMMANDS:
  corpus       build | diff | index — manage the knowledge-base corpus
  dictionary   bootstrap | list — manage the CNL dictionary
  version      print version
  help         this message

Run "olifant <subcommand> --help" for subcommand details.`)
}
