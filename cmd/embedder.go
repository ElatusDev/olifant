// Package cmd — `olifant embedder` subcommand dispatches Phase B1b/B1c
// embedder operations: training (on Modal) and held-out recall@5 evaluation
// (later, in B1c). This file wraps the Modal CLI for train + artefact
// movement; the actual training logic lives in
// `internal/embedder/modal_app.py`.
//
// HARD RULE: tooling-in-Go. Python is permitted only for the embedder
// training subprocess itself (sentence-transformers driver). All
// orchestration — uploads, run dispatch, pulls — is Go shelling out to
// `modal`.
package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// defaultModalApp is the on-disk path to modal_app.py relative to the
// olifant repo root. Override with `--app` for tests or alt locations.
const defaultModalApp = "internal/embedder/modal_app.py"

// defaultVolumeName matches modal_app.py's VOLUME_NAME constant.
const defaultVolumeName = "olifant-train-v1"

// defaultTriplesRemote is the path on the Modal volume where the JSONL
// lands. Matches modal_app.py's TRIPLES_PATH (under /data, which is the
// volume mountpoint inside the container).
const defaultTriplesRemote = "/embedder-v1/triples.jsonl"

// defaultModelLocal is where `pull` deposits the trained model dir.
const defaultModelLocalSub = ".olifant/training/embedder-v1/model"

// defaultModelRemote is the Modal-side dir to pull (under /embedders/v1/).
const defaultModelRemote = "/embedders/v1/model"

// Embedder dispatches `olifant embedder <train|pull|ls>`.
func Embedder(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant embedder: missing action (train|pull|ls)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "train":
		return embedderTrain(rest)
	case "pull":
		return embedderPull(rest)
	case "ls":
		return embedderLs(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant embedder: unknown action %q\n", action)
		return 2
	}
}

// runner is exec.Command by default; tests swap a stub.
var runner = func(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// embedderTrain (a) uploads the local triples JSONL to the Modal volume
// and (b) invokes `modal run modal_app.py::<train_full|dry_run>`.
//
// Uses the modal CLI as a subprocess (no Modal SDK in Go). Streams stdout/
// stderr from modal to the local terminal so progress bars are visible.
func embedderTrain(args []string) int {
	fs := flag.NewFlagSet("embedder train", flag.ExitOnError)
	triples := fs.String("triples", "", "local triples.jsonl path (default ~/.olifant/training/embedder-v1/triples.jsonl)")
	modalBin := fs.String("modal-bin", "modal", "modal CLI binary")
	appPath := fs.String("app", defaultModalApp, "modal app file path")
	volume := fs.String("volume", defaultVolumeName, "modal volume name")
	remote := fs.String("remote", defaultTriplesRemote, "remote path inside the modal volume")
	dryRun := fs.Bool("dry-run", false, "smoke-test on 100 examples (~3 min, ~$0.10)")
	skipUpload := fs.Bool("skip-upload", false, "skip volume put (use existing volume copy)")
	_ = fs.Parse(args)

	if *triples == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "embedder train: cannot resolve home dir:", err)
			return 1
		}
		*triples = filepath.Join(home, ".olifant", "training", "embedder-v1", "triples.jsonl")
	}
	if _, err := os.Stat(*triples); err != nil {
		fmt.Fprintf(os.Stderr, "embedder train: triples file not found: %s\n", *triples)
		return 1
	}

	if !*skipUpload {
		fmt.Fprintf(os.Stderr, "uploading %s → modal-volume %s%s\n", *triples, *volume, *remote)
		up := runner(*modalBin, "volume", "put", *volume, *triples, *remote, "--force")
		up.Stdout = os.Stdout
		up.Stderr = os.Stderr
		if err := up.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "modal volume put failed:", err)
			return 1
		}
	}

	entry := "train_full"
	if *dryRun {
		entry = "dry_run"
	}
	target := fmt.Sprintf("%s::%s", *appPath, entry)
	fmt.Fprintf(os.Stderr, "modal run %s\n", target)
	run := runner(*modalBin, "run", target)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "modal run failed:", err)
		return 1
	}
	return 0
}

// embedderPull copies the trained model dir from the modal volume to a
// local path so B1c can load it for the recall@5 evaluation.
func embedderPull(args []string) int {
	fs := flag.NewFlagSet("embedder pull", flag.ExitOnError)
	modalBin := fs.String("modal-bin", "modal", "modal CLI binary")
	volume := fs.String("volume", defaultVolumeName, "modal volume name")
	remote := fs.String("remote", defaultModelRemote, "remote dir to pull from")
	local := fs.String("local", "", "local destination dir (default ~/"+defaultModelLocalSub+")")
	_ = fs.Parse(args)

	if *local == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "embedder pull: cannot resolve home dir:", err)
			return 1
		}
		*local = filepath.Join(home, defaultModelLocalSub)
	}
	if err := os.MkdirAll(*local, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "embedder pull: mkdir failed:", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "modal volume get %s %s → %s\n", *volume, *remote, *local)
	cmd := runner(*modalBin, "volume", "get", *volume, *remote, *local, "--force")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "modal volume get failed:", err)
		return 1
	}
	return 0
}

// embedderLs invokes modal_app.py's ls entry-point to list the volume's
// /embedders/* contents — a debug helper after training.
func embedderLs(args []string) int {
	fs := flag.NewFlagSet("embedder ls", flag.ExitOnError)
	modalBin := fs.String("modal-bin", "modal", "modal CLI binary")
	appPath := fs.String("app", defaultModalApp, "modal app file path")
	_ = fs.Parse(args)

	target := fmt.Sprintf("%s::ls", *appPath)
	fmt.Fprintf(os.Stderr, "modal run %s\n", target)
	cmd := runner(*modalBin, "run", target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "modal run ls failed:", err)
		return 1
	}
	return 0
}
