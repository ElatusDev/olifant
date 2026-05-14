package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/psp"
)

// Run dispatches `olifant run --plan <file>`.
func Run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	planPath := fs.String("plan", "", "path to plan YAML file (required)")
	verbose := fs.Bool("v", false, "verbose protocol log to stderr")
	timeoutSec := fs.Int("timeout", 1800, "overall timeout in seconds (default 30 min)")
	synth := fs.String("synth", "", "executor model override (defaults to OLIFANT_SYNTHESIZER)")
	_ = fs.Parse(args)

	if *planPath == "" {
		fmt.Fprintln(os.Stderr, "olifant run: --plan is required")
		return 2
	}
	plan, err := psp.LoadPlan(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant run: load plan:", err)
		return 1
	}

	// Validate before executing — surfaces MPS cap, dep cycles, unknown deps.
	if verr := psp.Validate(plan); verr != nil {
		fmt.Fprintln(os.Stderr, "olifant run: plan invalid:", verr)
		// Hint: suggest splitting if MPS exceeded.
		if len(plan.Steps) > psp.MaxStepsPerPlan {
			fmt.Fprintf(os.Stderr, "  hint: this plan has %d steps; cap is %d. Run `olifant plan split %s` to partition into sub-plans.\n",
				len(plan.Steps), psp.MaxStepsPerPlan, *planPath)
		}
		return 1
	}

	// Resolve runtime endpoints + executor model.
	rt := config.Resolve()
	executorModel := rt.Synthesizer
	if *synth != "" {
		executorModel = *synth
	}
	executor := psp.NewLocalExecutor(rt.OllamaURL, executorModel)

	// Find kb root for short-term writes + signal resolution.
	kbRoot := ""
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot = filepath.Dir(found)
	}

	if *verbose {
		fmt.Fprintln(os.Stderr, "config:", rt.String())
		fmt.Fprintln(os.Stderr, "plan:  ", plan.PlanID, "—", len(plan.Steps), "steps —", plan.Goal)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	result, rerr := psp.Run(ctx, psp.RunnerConfig{
		Executor: executor,
		Plan:     plan,
		KBRoot:   kbRoot,
		Verbose:  *verbose,
	})
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "olifant run:", rerr)
		if result != nil && result.AggregatePath != "" {
			fmt.Fprintf(os.Stderr, "# partial aggregate: %s\n", result.AggregatePath)
		}
		return 1
	}

	fmt.Printf("plan %s — state=%s verdict=%s steps=%d attempts=%d elapsed=%dms\n",
		plan.PlanID, result.State, result.Aggregate.Verdict,
		result.Aggregate.TotalSteps, result.Aggregate.TotalAttempts,
		result.Aggregate.TotalElapsedMs)
	if result.AggregatePath != "" {
		fmt.Printf("aggregate: %s\n", result.AggregatePath)
	}
	if result.State == psp.StateClosedError {
		return 1
	}
	return 0
}
