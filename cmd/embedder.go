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
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/embedder"
	"github.com/ElatusDev/olifant/internal/ollama"
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

// Embedder dispatches `olifant embedder <train|pull|ls|recall>`.
func Embedder(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant embedder: missing action (train|pull|ls|recall)")
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
	case "recall":
		return embedderRecall(rest)
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

// defaultRecallRemoteDir is where the recall input JSONLs land on the
// Modal volume. Matches modal_app.py's RECALL_*_PATH (under /data).
const defaultRecallRemoteDir = "/embedder-v1/recall"

// embedderRecall runs the B1c recall@5 comparison: baseline embedder via
// Ollama locally, candidate embedder server-side on Modal, both ranked
// against the full prose corpus; emits recall-at-5-report.json and a
// gate GB1 summary.
func embedderRecall(args []string) int {
	fs := flag.NewFlagSet("embedder recall", flag.ExitOnError)
	queriesPath := fs.String("queries", "", "recall suite YAML (required)")
	proseDir := fs.String("prose-dir", "", "v2-curriculum prose dir (default: <kb-root>/corpus/v2-curriculum/prose)")
	kbRoot := fs.String("kb-root", "", "knowledge-base root (default: autodetect via cwd ancestors)")
	baseline := fs.String("baseline", "nomic", "baseline embedder (only `nomic` supported: Ollama-served)")
	candidate := fs.String("candidate", "v1", "candidate embedder (only `v1` supported: Modal-served)")
	topK := fs.Int("top-k", 10, "hits recorded per query (recall is scored at 5)")
	batch := fs.Int("batch", 64, "Ollama embed batch size")
	out := fs.String("out", "", "report path (default ~/.olifant/training/embedder-v1/recall-at-5-report.json)")
	modalBin := fs.String("modal-bin", "modal", "modal CLI binary")
	appPath := fs.String("app", defaultModalApp, "modal app file path")
	volume := fs.String("volume", defaultVolumeName, "modal volume name")
	skipUpload := fs.Bool("skip-upload", false, "skip volume put of recall inputs (use existing volume copies)")
	ollamaCandidate := fs.String("ollama-candidate", "", "evaluate an arbitrary Ollama-served embedding model as the candidate vs the OLIFANT_EMBEDDER baseline; skips the Modal path (F2/#13)")
	_ = fs.Parse(args)

	if *queriesPath == "" {
		fmt.Fprintln(os.Stderr, "embedder recall: --queries is required")
		return 2
	}
	if *ollamaCandidate == "" && (*baseline != "nomic" || *candidate != "v1") {
		fmt.Fprintf(os.Stderr, "embedder recall: unsupported pair baseline=%q candidate=%q\n", *baseline, *candidate)
		return 2
	}

	suite, err := embedder.LoadRecallSuite(*queriesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: load suite:", err)
		return 1
	}

	prose := *proseDir
	if prose == "" {
		root := *kbRoot
		if root == "" {
			if found, ok := findUp("knowledge-base/README.md"); ok {
				root = filepath.Dir(found)
			}
		}
		if root == "" {
			fmt.Fprintln(os.Stderr, "embedder recall: --prose-dir or --kb-root required (autodetect failed)")
			return 2
		}
		prose = filepath.Join(root, "corpus", "v2-curriculum", "prose")
	}
	sents, err := embedder.LoadProse(prose)
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: load prose:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "suite %s: %d queries; corpus: %d sentences\n", suite.SuiteID, len(suite.Queries), len(sents))

	reportPath := *out
	if reportPath == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintln(os.Stderr, "embedder recall: cannot resolve home dir:", herr)
			return 1
		}
		reportPath = filepath.Join(home, ".olifant", "training", "embedder-v1", "recall-at-5-report.json")
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: mkdir:", err)
		return 1
	}

	cfg := config.Resolve()
	var baseResults, candResults []embedder.QueryResult
	var baseName, candName string
	if *ollamaCandidate != "" {
		var rc int
		baseResults, rc = recallOllama(suite.Queries, sents, *topK, *batch, cfg.Embedder)
		if rc != 0 {
			return rc
		}
		candResults, rc = recallOllama(suite.Queries, sents, *topK, *batch, *ollamaCandidate)
		if rc != 0 {
			return rc
		}
		baseName = cfg.Embedder + " (ollama)"
		candName = *ollamaCandidate + " (ollama)"
	} else {
		var rc int
		candResults, rc = recallCandidate(suite.Queries, sents, *topK, *modalBin, *appPath, *volume, *skipUpload, filepath.Dir(reportPath))
		if rc != 0 {
			return rc
		}
		baseResults, rc = recallOllama(suite.Queries, sents, *topK, *batch, cfg.Embedder)
		if rc != 0 {
			return rc
		}
		baseName = cfg.Embedder + " (ollama)"
		candName = "domain-v1 (modal)"
	}

	report := embedder.BuildReport(suite.SuiteID,
		embedder.EmbedderRecall{Name: baseName, Recall5: embedder.RecallAt(baseResults, embedder.RecallK), Results: baseResults},
		embedder.EmbedderRecall{Name: candName, Recall5: embedder.RecallAt(candResults, embedder.RecallK), Results: candResults},
	)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: marshal report:", err)
		return 1
	}
	if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: write report:", err)
		return 1
	}

	fmt.Printf("recall@5  baseline  %-28s %.3f\n", report.Baseline.Name, report.Baseline.Recall5)
	fmt.Printf("recall@5  candidate %-28s %.3f\n", report.Candidate.Name, report.Candidate.Recall5)
	fmt.Printf("relative improvement: %+.1f%%  (gate GB1 threshold: ≥%+.0f%%)\n",
		report.RelativeImprovement*100, report.GateThreshold*100)
	if report.GatePass {
		fmt.Println("gate GB1: PASS (user GO/NO-GO still required)")
	} else {
		fmt.Println("gate GB1: FAIL")
	}
	fmt.Println("report:", reportPath)
	return 0
}

// recallOllama embeds queries + corpus via the named Ollama-served
// embedding model and ranks locally.
func recallOllama(queries []embedder.Query, sents []embedder.Sentence, topK, batch int, model string) ([]embedder.QueryResult, int) {
	cfg := config.Resolve()
	oc := ollama.New(cfg.OllamaURL)
	defer oc.CloseIdle()
	ctx := context.Background()

	texts := make([]string, len(sents))
	for i, s := range sents {
		texts[i] = s.Text
	}
	sentVecs := make([][]float32, 0, len(sents))
	start := time.Now()
	for lo := 0; lo < len(texts); lo += batch {
		hi := lo + batch
		if hi > len(texts) {
			hi = len(texts)
		}
		vecs, err := oc.Embed(ctx, model, texts[lo:hi])
		if err != nil {
			fmt.Fprintf(os.Stderr, "embedder recall: ollama embed sentences [%d:%d]: %v\n", lo, hi, err)
			return nil, 1
		}
		sentVecs = append(sentVecs, vecs...)
		if lo/batch%10 == 0 {
			fmt.Fprintf(os.Stderr, "%s: embedded %d/%d sentences (%.0fs)\n", model, hi, len(texts), time.Since(start).Seconds())
		}
	}

	results := make([]embedder.QueryResult, 0, len(queries))
	for _, q := range queries {
		qv, err := oc.Embed(ctx, model, []string{q.Text})
		if err != nil {
			fmt.Fprintf(os.Stderr, "embedder recall: ollama embed query %s: %v\n", q.ID, err)
			return nil, 1
		}
		results = append(results, embedder.QueryResult{
			QueryID:        q.ID,
			ExpectedSource: q.ExpectedSource,
			Hits:           embedder.TopK(qv[0], sentVecs, sents, topK),
		})
	}
	embedder.ScoreResults(results)
	return results, 0
}

// recallCandidate uploads the recall inputs to the Modal volume, invokes
// modal_app.py::recall_embed, and parses the marker-delimited JSON the
// remote function prints.
func recallCandidate(queries []embedder.Query, sents []embedder.Sentence, topK int, modalBin, appPath, volume string, skipUpload bool, workDir string) ([]embedder.QueryResult, int) {
	sentsLocal := filepath.Join(workDir, "recall-sentences.jsonl")
	queriesLocal := filepath.Join(workDir, "recall-queries.jsonl")
	if err := embedder.WriteRecallInputs(sentsLocal, queriesLocal, sents, queries); err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: write inputs:", err)
		return nil, 1
	}

	if !skipUpload {
		uploads := []struct{ local, remote string }{
			{sentsLocal, defaultRecallRemoteDir + "/sentences.jsonl"},
			{queriesLocal, defaultRecallRemoteDir + "/queries.jsonl"},
		}
		for _, u := range uploads {
			local, remote := u.local, u.remote
			fmt.Fprintf(os.Stderr, "uploading %s → modal-volume %s%s\n", local, volume, remote)
			up := runner(modalBin, "volume", "put", volume, local, remote, "--force")
			up.Stdout = os.Stdout
			up.Stderr = os.Stderr
			if err := up.Run(); err != nil {
				fmt.Fprintln(os.Stderr, "modal volume put failed:", err)
				return nil, 1
			}
		}
	}

	target := fmt.Sprintf("%s::recall_embed", appPath)
	fmt.Fprintf(os.Stderr, "modal run %s --top-k %d\n", target, topK)
	run := runner(modalBin, "run", target, "--top-k", fmt.Sprint(topK))
	var stdout bytes.Buffer
	run.Stdout = &stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "modal run recall_embed failed:", err)
		return nil, 1
	}

	results, err := embedder.ParseRemoteRecall(stdout.Bytes(), queries)
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedder recall: parse modal output:", err)
		return nil, 1
	}
	return results, 0
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
