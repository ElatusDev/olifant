package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ElatusDev/olifant/internal/harvest"
)

// defaultThreshold is the GE-0 data-volume floor (harvest-v1 IA7): below it
// harvest refuses rather than emit noise from a near-empty signal.
const defaultThreshold = 15

// Harvest dispatches `olifant harvest [run|accept]` (default run) — BK-F5:
// mine reactions ⨝ turns into human-gated proposals. Propose-only (D-HV1/2),
// deterministic + LLM-free (D-HV6), accept-only labels (D-HV3), cursor dedup
// (D-HV5).
func Harvest(args []string) int {
	if len(args) > 0 && args[0] == "accept" {
		return harvestAccept(args[1:])
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	return harvestRun(args)
}

func harvestPaths() (reactions, cursorPath, outDir string) {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".olifant")
	return filepath.Join(base, "reactions.jsonl"),
		filepath.Join(base, "harvest", "cursor"),
		filepath.Join(base, "harvest")
}

func harvestRun(args []string) int {
	fs := flag.NewFlagSet("harvest", flag.ExitOnError)
	defReactions, defCursor, defOut := harvestPaths()
	reactionsPath := fs.String("reactions", defReactions, "reactions side-log path")
	cursorPath := fs.String("cursor", defCursor, "cursor path (delete to re-harvest)")
	out := fs.String("out", defOut, "report output directory")
	threshold := fs.Int("threshold", defaultThreshold, "GE-0 minimum effective signals")
	dryRun := fs.Bool("dry-run", false, "report only; do not update the cursor")
	_ = fs.Parse(args)

	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant harvest: knowledge-base not found in cwd ancestors")
		return 2
	}
	kbRoot := filepath.Dir(found)

	reactions, skipped, err := harvest.LoadReactions(*reactionsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant harvest:", err)
		return 2
	}
	effective, unjoinable := harvest.Effective(reactions)

	if len(effective) < *threshold {
		fmt.Fprintf(os.Stderr,
			"olifant harvest: GE-0 data-volume gate — %d effective signals < threshold %d.\n"+
				"Let the bookends + /retro labeling accumulate; this is the BK-F5 deferral, not a failure.\n",
			len(effective), *threshold)
		return 1
	}

	signals := harvest.Join(effective, kbRoot)
	var missing []string
	for _, s := range signals {
		if s.Turn == nil {
			missing = append(missing, s.Reaction.TurnID)
		}
	}

	cursor, err := harvest.LoadCursor(*cursorPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant harvest:", err)
		return 2
	}
	proposals := harvest.Classify(signals, cursor)

	reportPath, err := harvest.WriteReport(*out, signals, proposals, unjoinable, missing)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant harvest:", err)
		return 2
	}
	if !*dryRun {
		if err := harvest.SaveCursor(*cursorPath, cursor, proposals); err != nil {
			fmt.Fprintln(os.Stderr, "olifant harvest: cursor:", err)
			return 2
		}
	}

	fmt.Println(reportPath)
	fmt.Fprintf(os.Stderr,
		"# signals=%d proposals=%d (skipped-lines=%d unjoinable=%d missing-turns=%d) dry-run=%v\n",
		len(signals), len(proposals), len(skipped), unjoinable, len(missing), *dryRun)
	return 0
}

// harvestAccept promotes ONE eval-case proposal to the real-usage suite —
// the explicit human action of D-HV1. Only eval-case proposals are
// machine-promotable; the other kinds name their manual gated homes in the
// report.
func harvestAccept(args []string) int {
	fs := flag.NewFlagSet("harvest accept", flag.ExitOnError)
	defReactions, _, _ := harvestPaths()
	reactionsPath := fs.String("reactions", defReactions, "reactions side-log path")
	turnID := fs.String("turn", "", "turn_id of the eval-case proposal to accept")
	suite := fs.String("suite", "", "target suite file (default: <kb-root>/eval/suites/real-usage-v1.yaml; NEVER code-feeding-v2 — D-HV4)")
	_ = fs.Parse(args)

	if *turnID == "" {
		fmt.Fprintln(os.Stderr, "olifant harvest accept: -turn required")
		return 2
	}
	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant harvest accept: knowledge-base not found")
		return 2
	}
	kbRoot := filepath.Dir(found)
	if *suite == "" {
		*suite = filepath.Join(kbRoot, "eval", "suites", "real-usage-v1.yaml")
	}
	if strings.Contains(*suite, "code-feeding") {
		fmt.Fprintln(os.Stderr, "olifant harvest accept: refusing to write into code-feeding-* (D-HV4)")
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(*suite), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "olifant harvest accept:", err)
		return 2
	}

	reactions, _, err := harvest.LoadReactions(*reactionsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant harvest accept:", err)
		return 2
	}
	effective, _ := harvest.Effective(reactions)
	signals := harvest.Join(effective, kbRoot)
	for _, p := range harvest.Classify(signals, nil) {
		if p.TurnID == *turnID && p.Kind == harvest.KindEvalCase {
			entry := harvest.CaseYAML(p)
			f, err := os.OpenFile(*suite, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				fmt.Fprintln(os.Stderr, "olifant harvest accept:", err)
				return 2
			}
			defer f.Close()
			if st, _ := f.Stat(); st != nil && st.Size() == 0 {
				if _, err := f.WriteString("suite_id: real-usage-v1\ncases:\n"); err != nil {
					fmt.Fprintln(os.Stderr, "olifant harvest accept:", err)
					return 2
				}
			}
			if _, err := f.WriteString(entry); err != nil {
				fmt.Fprintln(os.Stderr, "olifant harvest accept:", err)
				return 2
			}
			fmt.Printf("accepted %s → %s\n", p.ID, *suite)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "olifant harvest accept: no eval-case proposal for turn %q (check the report)\n", *turnID)
	return 1
}
