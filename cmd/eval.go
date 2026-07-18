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
	"github.com/ElatusDev/olifant/internal/kbtree"
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

// suiteSpec describes one member of the gate's suite set (HV-F1 D-RG1):
// identity, location, and the thresholds it is judged by.
type suiteSpec struct {
	ID   string
	Path string
	Cfg  eval.GateConfig
	// DeriveMinClean sets MinClean = len(cases) at load time, so growing
	// the suite via `harvest accept` never needs a threshold edit (D-RG3).
	DeriveMinClean bool
	// Optional suites degrade to a named SKIPPED receipt when the file is
	// absent (a stale KB checkout must be visible, not fatal — D-RG5).
	Optional bool
}

// defaultSuiteSet is the ordered set `eval gate` / `eval gate-check` cover
// when no -suite is given: the locked synthetic baseline plus the
// harvest-accepted real-usage suite.
func defaultSuiteSet(kbRoot string) []suiteSpec {
	dir := filepath.Join(kbRoot, "eval", "suites")
	return []suiteSpec{
		{
			ID:   "code-feeding-v2",
			Path: filepath.Join(dir, "code-feeding-v2.yaml"),
			Cfg:  eval.GateConfig{MinClean: 11, MaxBlockers: 0, MinFirstTry: 0.70},
		},
		{
			ID:   "real-usage-v1",
			Path: filepath.Join(dir, "real-usage-v1.yaml"),
			// MinFirstTry 0.25 ratified at GRG from the 2026-07-06 baseline
			// (4/4 clean, 2/4 first-try): at N=4 a 0.70 floor is one
			// nondeterministic retry away from a false FAIL; 0.25 still
			// catches the collapse-to-all-retries AP103 signature.
			// Re-ratify as harvest accepts grow the suite (revisit at N≥8).
			Cfg:            eval.GateConfig{MaxBlockers: 0, MinFirstTry: 0.25},
			DeriveMinClean: true,
			Optional:       true,
		},
	}
}

// evalGate implements `olifant eval gate` (#16 E1): run the suite set (or a
// single -suite / -report, back-compat), apply per-suite count thresholds,
// write one suite-scoped receipt each.
func evalGate(args []string) int {
	fs := flag.NewFlagSet("eval gate", flag.ExitOnError)
	suitePath := fs.String("suite", "", "single suite YAML (default: the full suite set under <kb-root>/eval/suites/)")
	reportDir := fs.String("report", "", "judge an existing run dir instead of running the suite (single-suite mode)")
	baselineDir := fs.String("baseline", "", "baseline run dir (single-suite mode; default: run dir of the suite's newest PASS receipt)")
	minClean := fs.Int("min-clean", 11, "minimum clean cases (D-EG1; single-suite mode)")
	maxBlockers := fs.Int("max-blockers", 0, "maximum total BLOCKERs (D-EG1; single-suite mode)")
	minFirstTry := fs.Float64("min-first-try", 0.70, "minimum first-try pass rate 0..1; 0 disables (single-suite mode)")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 3600, "per-suite timeout in seconds")
	notify := fs.Bool("notify", false, "nightly mode: append to drift.log; macOS notification on FAIL (D-EG5)")
	kbRootFlag := fs.String("kb-root", "", "resolve suites, corpus manifest, and cite validation against this KB tree (default: findUp); pin to a clean checkout when the shared knowledge-base symlink is on a foreign branch (olifant#71)")
	_ = fs.Parse(args)

	kbRoot, platformRoot := resolveRoots(*kbRootFlag)
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "olifant eval gate: kb-root not found (run from the platform tree, or pass -kb-root)")
		return gateExitUsage
	}

	corpusSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "manifest.yaml"))
	repoSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "repo-manifest.yaml"))
	env := gateEnv{
		kbRoot:       kbRoot,
		platformRoot: platformRoot,
		logPath:      receiptsLogPath(),
		baselineDir:  *baselineDir,
		base:         eval.Receipt{GitSHA: headSHA(), CorpusSHA: corpusSHA, RepoSHA: repoSHA, Timestamp: time.Now().UTC().Format(time.RFC3339)},
		verbose:      *verbose,
		notify:       *notify,
		timeoutSec:   *timeoutSec,
	}

	// Single-suite / judge-report mode (back-compat): flags set the thresholds.
	if *suitePath != "" || *reportDir != "" {
		if *suitePath == "" {
			*suitePath = filepath.Join(kbRoot, "eval", "suites", "code-feeding-v2.yaml")
		}
		spec := suiteSpec{Path: *suitePath, Cfg: eval.GateConfig{MinClean: *minClean, MaxBlockers: *maxBlockers, MinFirstTry: *minFirstTry}}
		return gateOneSuite(env, spec, *reportDir, false)
	}

	// Suite-set mode (HV-F1 D-RG1): preflight once, then every suite in
	// order; PASS only when each judged suite passes.
	if reason := gatePreflight(); reason != "" {
		fmt.Fprintf(os.Stderr, "GATE SKIPPED (deps): %s\n", reason)
		for _, spec := range defaultSuiteSet(kbRoot) {
			skipped := env.base
			skipped.SuiteID = spec.ID
			skipped.SuiteSHA, _ = eval.FileSHA256(spec.Path)
			skipped.Verdict = "SKIPPED"
			skipped.OverrideReason = reason
			_ = eval.WriteReceipt("", env.logPath, skipped)
		}
		if *notify {
			driftLog("SKIPPED", reason)
		}
		return gateExitSkipped
	}
	anyFail, anySkip, anyUsage := false, false, false
	for _, spec := range defaultSuiteSet(kbRoot) {
		switch gateOneSuite(env, spec, "", true) {
		case gateExitFail:
			anyFail = true
		case gateExitSkipped:
			anySkip = true
		case gateExitUsage:
			anyUsage = true
		}
	}
	switch {
	case anyFail:
		return gateExitFail
	case anyUsage:
		return gateExitUsage
	case anySkip:
		return gateExitSkipped
	default:
		return gateExitPass
	}
}

// gateEnv carries the invocation-wide state gateOneSuite needs: roots,
// receipt log, the receipt fields shared by every suite, and output flags.
type gateEnv struct {
	kbRoot, platformRoot, logPath, baselineDir string
	base                                       eval.Receipt
	verbose, notify                            bool
	timeoutSec                                 int
}

// gateOneSuite runs (or judges, when reportDir != "") one suite, applies its
// thresholds, and mints its suite-scoped receipt. The returned code covers
// this suite alone; the caller aggregates.
func gateOneSuite(env gateEnv, spec suiteSpec, reportDir string, preflighted bool) int {
	suiteSHA, err := eval.FileSHA256(spec.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate: suite fingerprint:", err)
		return gateExitUsage
	}
	receipt := env.base
	receipt.SuiteID = spec.ID
	receipt.SuiteSHA = suiteSHA

	// Absent suite file: named degrade for optional suites (D-RG5 — a stale
	// KB checkout must be visible, not fatal), hard usage error otherwise.
	if suiteSHA == "" && reportDir == "" {
		if !spec.Optional {
			fmt.Fprintf(os.Stderr, "olifant eval gate: suite missing: %s\n", spec.Path)
			return gateExitUsage
		}
		receipt.Verdict = "SKIPPED"
		receipt.OverrideReason = "suite file missing at " + spec.Path
		_ = eval.WriteReceipt("", env.logPath, receipt)
		fmt.Fprintf(os.Stderr, "eval gate SKIPPED suite=%s — file missing at %s (stale KB checkout?)\n", spec.ID, spec.Path)
		if env.notify {
			driftLog("SKIPPED", "suite="+spec.ID+" file missing")
		}
		return gateExitPass // named skip: the required suites still decide the verdict
	}

	var report *eval.Report
	runDir := reportDir
	if reportDir != "" {
		report, err = eval.LoadReport(reportDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load report:", err)
			return gateExitUsage
		}
	} else {
		if !preflighted {
			if reason := gatePreflight(); reason != "" {
				fmt.Fprintf(os.Stderr, "GATE SKIPPED (deps): %s\n", reason)
				receipt.Verdict = "SKIPPED"
				receipt.OverrideReason = reason
				_ = eval.WriteReceipt("", env.logPath, receipt)
				if env.notify {
					driftLog("SKIPPED", reason)
				}
				return gateExitSkipped
			}
		}
		suite, lerr := eval.LoadSuite(spec.Path)
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load suite:", lerr)
			return gateExitUsage
		}
		if spec.DeriveMinClean {
			spec.Cfg.MinClean = len(suite.Cases) // scale-free: harvest accept grows the bar (D-RG3)
		}
		fmt.Fprintf(os.Stderr, "eval gate: suite=%s path=%s cases=%d\n", suite.SuiteID, spec.Path, len(suite.Cases))
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(env.timeoutSec)*time.Second)
		defer cancel()
		var runErr error
		report, runErr = eval.Run(ctx, eval.RunConfig{
			Suite:        suite,
			PlatformRoot: env.platformRoot,
			KBRoot:       env.kbRoot,
			OutDir:       filepath.Join(env.kbRoot, "short-term", "eval-runs"),
			Verbose:      env.verbose,
		})
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate:", runErr)
			return gateExitFail
		}
		runDir = filepath.Join(env.kbRoot, "short-term", "eval-runs", report.RunID)
	}
	// The file's declared suite_id is the lineage identity (D-RG2).
	if report.SuiteID != "" {
		receipt.SuiteID = report.SuiteID
	}

	var baseline *eval.Report
	switch {
	case env.baselineDir != "":
		baseline, err = eval.LoadReport(env.baselineDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate: load baseline:", err)
			return gateExitUsage
		}
	default:
		// Suite-scoped baseline (D-RG2): a PASS from another suite is never
		// this suite's no-new-blocker reference; pre-suite_id receipts are
		// excluded by the filter, so the first scoped run simply has none.
		if rec, _ := eval.LatestReceipt(env.logPath, eval.Receipt{Verdict: "PASS", SuiteID: receipt.SuiteID}); rec != nil && rec.RunDir != "" {
			baseline, _ = eval.LoadReport(rec.RunDir) // best-effort: missing baseline run skips the new-blocker check
		}
	}

	verdict := eval.Gate(report, baseline, spec.Cfg)

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
	if werr := eval.WriteReceipt(runDir, env.logPath, receipt); werr != nil {
		fmt.Fprintln(os.Stderr, "olifant eval gate: write receipt:", werr)
	}

	fmt.Printf("eval gate %s suite=%s — clean %d/%d, BLOCKERs %d (thresholds: clean ≥ %d, B ≤ %d)\n",
		receipt.Verdict, receipt.SuiteID, report.CleanCases, report.TotalCases, report.TotalBlockers, spec.Cfg.MinClean, spec.Cfg.MaxBlockers)
	if env.notify {
		summary := fmt.Sprintf("suite=%s clean=%d/%d B=%d run=%s", receipt.SuiteID, report.CleanCases, report.TotalCases, report.TotalBlockers, report.RunID)
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
		if rb := eval.FirstAttemptBlockerReport(report); rb != "" {
			fmt.Println("First-attempt BLOCKERs (retry-masked from final counts; see AP103 for the canonical detection workflow):")
			fmt.Print(rb)
			fmt.Println()
		}
		fmt.Print(eval.DiffTable(report, baseline))
		return gateExitFail
	}
	return gateExitPass
}

// resolveRoots returns (kbRoot, platformRoot). platformRoot is ALWAYS the real
// platform root from findUp (so repo-path cites resolve correctly). kbRoot is
// resolved by precedence: the -kb-root flag > the OLIFANT_KB_ROOT env >
// findUp (olifant#71 / D-EV2, olifant#74 / D-PG1). The env lets the pre-push
// hook's bare `gate-check` and the minting `eval gate` agree on one pinned
// clean checkout — without a per-call flag — while the shared knowledge-base
// symlink is on a foreign branch. The env NEVER moves platformRoot.
func resolveRoots(kbRootFlag string) (kbRoot, platformRoot string) {
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot = filepath.Dir(found)
		platformRoot = filepath.Dir(kbRoot)
	}
	override := kbRootFlag
	if override == "" {
		override = os.Getenv("OLIFANT_KB_ROOT")
	}
	if override != "" {
		if abs, err := filepath.Abs(override); err == nil {
			kbRoot = abs
		} else {
			kbRoot = override
		}
	}
	return kbRoot, platformRoot
}

// resolveKBTree builds the tree KB cite resolution reads from. With gitRef set,
// KB cites resolve from that ref's blobs (git run in the KB checkout dir) —
// decoupled from checkout state, no pinned worktree (olifant#90 / EV-F1);
// otherwise from the working tree (kbtree.FS). platformRoot (for repo cites) is
// always the real filesystem root (D227). Returns kbRoot=="" when the KB dir is
// not found; a non-nil error means the git ref was bad (never a silent
// working-tree fallback, D-GR4).
func resolveKBTree(kbRootFlag, gitRef string) (kb kbtree.Tree, kbRoot, platformRoot string, err error) {
	kbRoot, platformRoot = resolveRoots(kbRootFlag)
	if kbRoot == "" {
		return nil, "", "", nil
	}
	if strings.TrimSpace(gitRef) != "" {
		gt, gErr := kbtree.Git(kbRoot, gitRef)
		if gErr != nil {
			return nil, kbRoot, platformRoot, gErr
		}
		return gt, kbRoot, platformRoot, nil
	}
	return kbtree.FS(kbRoot), kbRoot, platformRoot, nil
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
	suitePath := fs.String("suite", "", "single suite YAML (default: the full suite set under <kb-root>/eval/suites/)")
	kbRootFlag := fs.String("kb-root", "", "resolve suites + corpus manifest against this KB tree (default: findUp; olifant#71)")
	_ = fs.Parse(args)

	kbRoot, _ := resolveRoots(*kbRootFlag)
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "olifant eval gate-check: kb-root not found (run from the platform tree, or pass -kb-root)")
		return gateExitUsage
	}
	specs := defaultSuiteSet(kbRoot)
	if *suitePath != "" {
		specs = []suiteSpec{{Path: *suitePath}}
	}
	corpusSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "manifest.yaml"))
	repoSHA, _ := eval.FileSHA256(filepath.Join(kbRoot, "corpus", "v1", "repo-manifest.yaml"))
	gitSHA := headSHA()
	logPath := receiptsLogPath()

	// Audited override (D-EG4): the only sanctioned bypass — one audited
	// line per suite in the checked set.
	if reason := os.Getenv("OLIFANT_EVAL_GATE_SKIP"); reason != "" {
		for _, spec := range specs {
			suiteSHA, _ := eval.FileSHA256(spec.Path)
			_ = eval.WriteReceipt("", logPath, eval.Receipt{
				Verdict: "OVERRIDE", SuiteID: spec.ID, GitSHA: gitSHA, SuiteSHA: suiteSHA, CorpusSHA: corpusSHA, RepoSHA: repoSHA,
				OverrideReason: reason, Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
		}
		fmt.Printf("eval gate-check OVERRIDE (audited): %s\n", reason)
		return gateExitPass
	}

	stale := false
	for _, spec := range specs {
		suiteSHA, err := eval.FileSHA256(spec.Path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate-check: suite fingerprint:", err)
			return gateExitUsage
		}
		if suiteSHA == "" {
			// Absent file: named degrade for optional suites (D-RG5); an
			// unconstrained-SHA receipt match would be a silent pass.
			if spec.Optional {
				fmt.Fprintf(os.Stderr, "eval gate-check SKIP suite=%s — file missing at %s (stale KB checkout?)\n", spec.ID, spec.Path)
				continue
			}
			fmt.Fprintf(os.Stderr, "olifant eval gate-check: suite missing: %s\n", spec.Path)
			return gateExitUsage
		}
		rec, err := eval.LatestReceipt(logPath, eval.Receipt{
			Verdict: "PASS", SuiteID: spec.ID, GitSHA: gitSHA, SuiteSHA: suiteSHA, CorpusSHA: corpusSHA, RepoSHA: repoSHA,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant eval gate-check: read receipts:", err)
			return gateExitUsage
		}
		if rec == nil {
			fmt.Printf("eval gate-check STALE suite=%s: no PASS receipt for HEAD %.12s with current suite+corpus+repo fingerprints\n",
				suiteLabel(spec), gitSHA)
			stale = true
			continue
		}
		fmt.Printf("eval gate-check FRESH suite=%s: run %s (clean %d/%d, B %d)\n",
			suiteLabel(spec), rec.RunID, rec.CleanCases, rec.TotalCases, rec.TotalBlockers)
	}
	if stale {
		return gateExitFail
	}
	return gateExitPass
}

// suiteLabel names a spec for output: the set entries carry an ID; a bare
// -suite path is labeled by its filename.
func suiteLabel(spec suiteSpec) string {
	if spec.ID != "" {
		return spec.ID
	}
	return filepath.Base(spec.Path)
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
