package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ElatusDev/olifant/internal/psp"
	"gopkg.in/yaml.v3"
)

// Plan dispatches `olifant plan <validate|split|stats>`.
func Plan(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant plan: missing action (validate|split|stats)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "validate":
		return planValidate(rest)
	case "split":
		return planSplit(rest)
	case "stats":
		fmt.Fprintln(os.Stderr, "olifant plan stats: not yet implemented (waits on ledger data)")
		return 1
	default:
		fmt.Fprintf(os.Stderr, "olifant plan: unknown action %q\n", action)
		return 2
	}
}

func planValidate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant plan validate: missing path")
		return 2
	}
	p, err := psp.LoadPlan(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant plan validate: load:", err)
		return 1
	}
	if err := psp.Validate(p); err != nil {
		fmt.Fprintln(os.Stderr, "INVALID:", err)
		return 1
	}
	fmt.Printf("OK — plan_id=%s steps=%d goal=%q\n", p.PlanID, len(p.Steps), p.Goal)
	return 0
}

func planSplit(args []string) int {
	fs := flag.NewFlagSet("plan split", flag.ExitOnError)
	out := fs.String("out", "", "output directory (default: same dir as input)")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "olifant plan split: missing path")
		return 2
	}
	p, err := psp.LoadPlan(rest[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant plan split: load:", err)
		return 1
	}
	parts := psp.Split(p)
	if parts == nil {
		fmt.Printf("plan %s has %d steps — under cap (%d); no split needed.\n",
			p.PlanID, len(p.Steps), psp.MaxStepsPerPlan)
		return 0
	}
	dir := *out
	if dir == "" {
		dir = filepath.Dir(rest[0])
	}
	for _, sub := range parts {
		name := strings.ReplaceAll(sub.PlanID, "/", "_") + ".yaml"
		path := filepath.Join(dir, name)
		body, merr := yaml.Marshal(sub)
		if merr != nil {
			fmt.Fprintln(os.Stderr, "marshal:", merr)
			return 1
		}
		if werr := os.WriteFile(path, body, 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "write:", werr)
			return 1
		}
		fmt.Printf("  wrote %s (%d steps)\n", path, len(sub.Steps))
	}
	fmt.Printf("split %s into %d sub-plans (session_id=%s).\n", p.PlanID, len(parts), p.PlanID)
	return 0
}
