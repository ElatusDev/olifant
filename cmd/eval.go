package cmd

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/eval"
)

// Eval dispatches `olifant eval <run|...>`.
func Eval(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant eval: missing action (run)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "run":
		return evalRun(rest)
	case "gate":
		return evalGate(rest)
	case "gate-check":
		return evalGateCheck(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant eval: unknown action %q\n", action)
		return 2
	}
}

// Gate exit codes (workflow D-EG6): deps-down is not a regression.
const (
	gateExitPass    = 0
	gateExitFail    = 1
	gateExitUsage   = 2
	gateExitSkipped = 3
)

// evalGate implements `olifant eval gate` (#16 E1): run the suite (or judge
// an existing run), apply count thresholds, write a receipt.
func evalGate(args []string) int {
	fs := flag.NewFlagSet("eval gate", flag.ExitOnError)
	suitePath := fs.String("suite", "", "suite YAML (default: <kb-root>/eval/suites/code-feeding-v2.yaml)")
	reportDir := fs.String("report", "", "judge an existing run dir instead of running the suite")
	baselineDir := fs.String("baseline", "", "baseline run dir (default: run dir of the newest PASS receipt)")
	minClean := fs.Int("min-clean", 11, "minimum clean cases (D-EG1)")
	maxBlockers := fs.Int("max-blockers", 0, "maximum total BLOCKERs (D-EG1)")
	minFirstTry := fs.Float64("min-first-try", 0.70, "minimum first-try pass rate 0..1; 0 disables (D-EG1 E4 amendment)")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 3600, "overall timeout in seconds")
	notify := fs.Bool("notify", false, "nightly mode: append to drift.log; macOS notification on FAIL (D-EG5)")
	_ = fs.Parse(args)

	kbRoot, platformRoot := "", ""
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot = filepath.Dir(found)
		platformRoot = filepath.Dir(kbRoot)
	}
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "olifant eval gate: kb-root not found (run from the platform tree)")
		return gateExitUsage
	}
	if *suitePath == "" {
		*suitePath = filepath.Join(kbRoot, "eval", "suites", "code-feeding-v2.yaml")
	}

	suiteSHA, err := eval.FileSHA256(*suitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate: suite fingerprint:", err)
		return gateExitUsage
	}
	corpusSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "manifest.yaml"))
	gitSHA := headSHA()
	logPath := receiptsLogPath()
	now := time.Now().UTC().Format(time.RFC3339)
	base := eval.Receipt{GitSHA: gitSHA, SuiteSHA: suiteSHA, CorpusSHA: corpusSHA, Timestamp: now}

	var report *eval.Report
	runDir := *reportDir
	if *reportDir != "" {
		report, err = eval.LoadReport(*reportDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load report:", err)
			return gateExitUsage
		}
	} else {
		if reason := gatePreflight(); reason != "" {
			fmt.Fprintf(os.Stderr, "GATE SKIPPED (deps): %s\n", reason)
			skipped := base
			skipped.Verdict = "SKIPPED"
			skipped.OverrideReason = reason
			_ = eval.WriteReceipt("", logPath, skipped)
			if *notify {
				driftLog("SKIPPED", reason)
			}
			return gateExitSkipped
		}
		suite, lerr := eval.LoadSuite(*suitePath)
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load suite:", lerr)
			return gateExitUsage
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
		defer cancel()
		var runErr error
		report, runErr = eval.Run(ctx, eval.RunConfig{
			Suite:        suite,
			PlatformRoot: platformRoot,
			KBRoot:       kbRoot,
			OutDir:       filepath.Join(kbRoot, "short-term", "eval-runs"),
			Verbose:      *verbose,
		})
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate:", runErr)
			return gateExitFail
		}
		runDir = filepath.Join(kbRoot, "short-term", "eval-runs", report.RunID)
	}

	var baseline *eval.Report
	switch {
	case *baselineDir != "":
		baseline, err = eval.LoadReport(*baselineDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load baseline:", err)
			return gateExitUsage
		}
	default:
		if rec, _ := eval.LatestReceipt(logPath, eval.Receipt{Verdict: "PASS"}); rec != nil && rec.RunDir != "" {
			baseline, _ = eval.LoadReport(rec.RunDir) // best-effort: missing baseline run skips the new-blocker check
		}
	}

	verdict := eval.Gate(report, baseline, eval.GateConfig{MinClean: *minClean, MaxBlockers: *maxBlockers, MinFirstTry: *minFirstTry})

	receipt := base
	receipt.RunID = report.RunID
	receipt.RunDir = runDir
	receipt.CleanCases = report.CleanCases
	receipt.TotalCases = report.TotalCases
	receipt.TotalBlockers = report.TotalBlockers
	if verdict.Pass {
		receipt.Verdict = "PASS"
	} else {
		receipt.Verdict = "FAIL"
	}
	if werr := eval.WriteReceipt(runDir, logPath, receipt); werr != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate: write receipt:", werr)
	}

	fmt.Printf("eval gate %s — clean %d/%d, BLOCKERs %d (thresholds: clean ≥ %d, B ≤ %d)\n",
		receipt.Verdict, report.CleanCases, report.TotalCases, report.TotalBlockers, *minClean, *maxBlockers)
	if *notify {
		summary := fmt.Sprintf("clean=%d/%d B=%d run=%s", report.CleanCases, report.TotalCases, report.TotalBlockers, report.RunID)
		driftLog(receipt.Verdict, summary)
		if !verdict.Pass {
			notifyMac("olifant eval gate", "FAIL — "+summary)
		}
	}
	if !verdict.Pass {
		for _, r := range verdict.Reasons {
			fmt.Println("  FAIL:", r)
		}
		fmt.Println()
		fmt.Print(eval.DiffTable(report, baseline))
		return gateExitFail
	}
	return gateExitPass
}

// driftLog appends a three-state (PASS/FAIL/SKIPPED) line to the nightly
// drift log (D-EG5).
func driftLog(state, detail string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".olifant", "eval-gate", "drift.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s %s\n", time.Now().UTC().Format(time.RFC3339), state, detail)
}

// notifyMac raises a macOS user notification; best-effort.
func notifyMac(title, message string) {
	script := fmt.Sprintf("display notification %q with title %q", message, title)
	_ = exec.Command("osascript", "-e", script).Run()
}

// evalGateCheck verifies a fresh PASS receipt exists for the current HEAD +
// suite + corpus fingerprints (the pre-push hook's fast path, D-EG2). Exit 0
// = fresh receipt (or audited override); exit 1 = stale/missing.
func evalGateCheck(args []string) int {
	fs := flag.NewFlagSet("eval gate-check", flag.ExitOnError)
	suitePath := fs.String("suite", "", "suite YAML (default: <kb-root>/eval/suites/code-feeding-v2.yaml)")
	_ = fs.Parse(args)

	kbRoot := ""
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot = filepath.Dir(found)
	}
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "olifant eval gate-check: kb-root not found")
		return gateExitUsage
	}
	if *suitePath == "" {
		*suitePath = filepath.Join(kbRoot, "eval", "suites", "code-feeding-v2.yaml")
	}
	suiteSHA, err := eval.FileSHA256(*suitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate-check: suite fingerprint:", err)
		return gateExitUsage
	}
	corpusSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "manifest.yaml"))
	gitSHA := headSHA()
	logPath := receiptsLogPath()

	// Audited override (D-EG4): the only sanctioned bypass.
	if reason := os.Getenv("OLIFANT_EVAL_GATE_SKIP"); reason != "" {
		_ = eval.WriteReceipt("", logPath, eval.Receipt{
			Verdict: "OVERRIDE", GitSHA: gitSHA, SuiteSHA: suiteSHA, CorpusSHA: corpusSHA,
			OverrideReason: reason, Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		fmt.Printf("eval gate-check OVERRIDE (audited): %s\n", reason)
		return gateExitPass
	}

	rec, err := eval.LatestReceipt(logPath, eval.Receipt{
		Verdict: "PASS", GitSHA: gitSHA, SuiteSHA: suiteSHA, CorpusSHA: corpusSHA,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate-check: read receipts:", err)
		return gateExitUsage
	}
	if rec == nil {
		fmt.Printf("eval gate-check STALE: no PASS receipt for HEAD %.12s with current suite+corpus fingerprints\n", gitSHA)
		return gateExitFail
	}
	fmt.Printf("eval gate-check FRESH: run %s (clean %d/%d, B %d)\n",
		rec.RunID, rec.CleanCases, rec.TotalCases, rec.TotalBlockers)
	return gateExitPass
}

// gatePreflight probes the eval's live dependencies; non-empty return =
// SKIPPED reason (D-EG6). The claude binary is only required when it is
// the active synth backend.
func gatePreflight() string {
	rt := config.Resolve()
	httpc := &http.Client{Timeout: 5 * time.Second}
	if resp, err := httpc.Get(rt.OllamaURL + "/api/version"); err != nil {
		return "ollama unreachable at " + rt.OllamaURL + " (tailscale up?): " + err.Error()
	} else {
		resp.Body.Close()
	}
	if resp, err := httpc.Get(rt.ChromaURL + "/api/v2/heartbeat"); err != nil {
		return "chroma unreachable at " + rt.ChromaURL + " (kubectl -n platform port-forward deploy/chromadb 8000:8000): " + err.Error()
	} else {
		resp.Body.Close()
	}
	if rt.SynthBackend == "claude" || rt.SynthBackend == "" {
		if _, ok := config.ResolveClaude(); !ok {
			return "claude binary not found (synth backend is claude)"
		}
	}
	return ""
}

// headSHA returns the current repo HEAD, empty when not in a git checkout.
func headSHA() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// receiptsLogPath is the append-only gate audit log (D-EG4).
func receiptsLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "olifant-eval-gate-receipts.log")
	}
	return filepath.Join(home, ".olifant", "eval-gate", "receipts.log")
}

func evalRun(args []string) int {
	fs := flag.NewFlagSet("eval run", flag.ExitOnError)
	suitePath := fs.String("suite", "", "path to suite YAML (required)")
	out := fs.String("out", "", "output directory (default: <kb-root>/short-term/eval-runs/)")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 3600, "overall timeout in seconds")
	retrieval := fs.String("retrieval", "", "RAG-pivot v2 collection name (e.g., olifant-v2-curriculum); empty = legacy v1 retrieval")
	_ = fs.Parse(args)

	if *suitePath == "" {
		fmt.Fprintln(os.Stderr, "olifant eval run: --suite is required")
		return 2
	}
	suite, err := eval.LoadSuite(*suitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant eval run: load suite:", err)
		return 1
	}

	// Locate kb + platform roots.
	kbRoot, platformRoot := "", ""
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot = filepath.Dir(found)
		platformRoot = filepath.Dir(kbRoot)
	}

	outDir := *out
	if outDir == "" && kbRoot != "" {
		outDir = filepath.Join(kbRoot, "short-term", "eval-runs")
	}
	if outDir == "" {
		fmt.Fprintln(os.Stderr, "olifant eval run: --out not specified and kb-root not found")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	report, runErr := eval.Run(ctx, eval.RunConfig{
		Suite:        suite,
		PlatformRoot: platformRoot,
		KBRoot:       kbRoot,
		OutDir:       outDir,
		Verbose:      *verbose,
		V2Collection: *retrieval,
	})
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "olifant eval run:", runErr)
		return 1
	}

	fmt.Println()
	fmt.Printf("eval run %s — suite=%s\n", report.RunID, report.SuiteID)
	fmt.Printf("  total cases:        %d\n", report.TotalCases)
	fmt.Printf("  clean cases:        %d / %d\n", report.CleanCases, report.TotalCases)
	fmt.Printf("  total BLOCKERs:     %d\n", report.TotalBlockers)
	fmt.Printf("  total WARNINGs:     %d\n", report.TotalWarnings)
	fmt.Printf("  total INFOs:        %d\n", report.TotalInfos)
	fmt.Printf("  first-try pass:     %.0f%%\n", report.FirstTryPassRate*100)
	if report.GradedPassRate != nil {
		fmt.Printf("  graded pass rate:   %.0f%%\n", *report.GradedPassRate*100)
	}
	fmt.Printf("  elapsed:            %s\n", time.Duration(report.ElapsedMs)*time.Millisecond)
	fmt.Println()
	fmt.Println("  per-case:")
	for _, c := range report.Cases {
		marker := " "
		if c.Blockers == 0 && c.Error == "" {
			marker = "✓"
		}
		if c.Error != "" {
			fmt.Printf("    %s %-44s ERROR: %s\n", marker, c.CaseID, c.Error)
		} else {
			fmt.Printf("    %s %-44s verdict=%-22s B=%d W=%d I=%d attempts=%d %s\n",
				marker, c.CaseID, c.Verdict, c.Blockers, c.Warnings, c.Infos,
				c.Attempts, fmtDur(c.ElapsedMs))
		}
	}
	fmt.Println()
	fmt.Printf("  report: %s\n", filepath.Join(outDir, report.RunID, "report.yaml"))
	if report.TotalBlockers > 0 {
		return 1
	}
	return 0
}

func fmtDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Truncate(time.Second).String()
}
