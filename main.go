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
	case "repo":
		os.Exit(cmd.Repo(args))
	case "challenge":
		os.Exit(cmd.Challenge(args))
	case "turn":
		os.Exit(cmd.Turn(args))
	case "plan":
		os.Exit(cmd.Plan(args))
	case "prompt":
		os.Exit(cmd.Prompt(args))
	case "run":
		os.Exit(cmd.Run(args))
	case "eval":
		os.Exit(cmd.Eval(args))
	case "validate":
		os.Exit(cmd.Validate(args))
	case "history":
		os.Exit(cmd.History(args))
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
  corpus       build | index — manage the knowledge-base corpus
  dictionary   bootstrap — manage the CNL dictionary
  repo         ingest — chunk + embed source from the 7 platform repos
  challenge    challenge "<request>" — step 0: produce a verdict in YAML
  turn         list | show | stats — inspect short-term event ledger
  plan         validate | split — manage prompt-plans (PSP v1)
  prompt       build "<goal>" — generate a PSP plan from a goal via RAG + synth
  run          --plan <file> — execute a prompt-plan via PSP runner
  eval         run --suite <file> — execute an eval suite battery
  validate     --claim <ref> --diff <ref> — post-Claude claim-vs-evidence audit
  history      scan — walk repo commit history and emit JSONL training data
  version      print version
  help         this message

Run "olifant <subcommand> --help" for subcommand details.`)
}
