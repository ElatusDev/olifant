package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	default:
		fmt.Fprintf(os.Stderr, "olifant eval: unknown action %q\n", action)
		return 2
	}
}

func evalRun(args []string) int {
	fs := flag.NewFlagSet("eval run", flag.ExitOnError)
	suitePath := fs.String("suite", "", "path to suite YAML (required)")
	out := fs.String("out", "", "output directory (default: <kb-root>/short-term/eval-runs/)")
	verbose := fs.Bool("v", false, "verbose progress")
	timeoutSec := fs.Int("timeout", 3600, "overall timeout in seconds")
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
