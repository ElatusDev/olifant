package cmd

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/promotion"
)

// Promote dispatches `olifant promote <status|set|demote>` — the charter §7
// promotion-state ledger for the challenge/validate verdict surfaces
// (olifant#87). `set` promotes a surface to blocking (naming its decision +
// receipt evidence); `demote` flips it back to advisory (the wired §7 demotion
// path, AC4). The enforcement in Challenge/Validate reads this ledger.
func Promote(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant promote: missing action (status|set|demote)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "status":
		return promoteStatus(rest)
	case "set":
		return promoteSet(rest)
	case "demote":
		return promoteDemote(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant promote: unknown action %q\n", action)
		return 2
	}
}

func promoteStatePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return promotion.DefaultPath()
}

func promoteStatus(args []string) int {
	fs := flag.NewFlagSet("promote status", flag.ExitOnError)
	statePath := fs.String("state", "", "promotion-state ledger path (default: ~/.olifant/promotion-state.yaml)")
	_ = fs.Parse(args)

	path, err := promoteStatePath(*statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote: %v\n", err)
		return 2
	}
	st, err := promotion.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote: %v\n", err)
		return 2
	}
	for _, name := range promotion.Surfaces {
		sf := st.Surfaces[name]
		status := promotion.StatusAdvisory
		extra := ""
		if sf != nil {
			if sf.Status != "" {
				status = sf.Status
			}
			if status == promotion.StatusBlocking {
				extra = fmt.Sprintf("  decision=%s receipts=%s promoted_at=%s",
					sf.Decision, strings.Join(sf.Receipts, ","), sf.PromotedAt)
			}
			if sf.Demotion != nil {
				extra += fmt.Sprintf("  last_demotion=%s (%s)", sf.Demotion.At, sf.Demotion.Reason)
			}
		}
		fmt.Printf("%-10s %s%s\n", name, status, extra)
	}
	return 0
}

func promoteSet(args []string) int {
	surface, rest, ok := leadingSurface(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant promote set: usage: olifant promote set <challenge|validate> --decision D250 --receipts id1,id2")
		return 2
	}
	fs := flag.NewFlagSet("promote set", flag.ExitOnError)
	statePath := fs.String("state", "", "promotion-state ledger path (default: ~/.olifant/promotion-state.yaml)")
	decision := fs.String("decision", "", "the §7 decision id (e.g. D250) — required")
	receipts := fs.String("receipts", "", "comma-separated receipt run_ids (the 2-PASS streak) — required")
	_ = fs.Parse(rest)

	path, err := promoteStatePath(*statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote: %v\n", err)
		return 2
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := promotion.Promote(path, surface, *decision, splitCSV(*receipts), now); err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote set: %v\n", err)
		return 2
	}
	fmt.Printf("promoted %s -> blocking (decision=%s, receipts=%d)\n", surface, *decision, len(splitCSV(*receipts)))
	return 0
}

func promoteDemote(args []string) int {
	surface, rest, ok := leadingSurface(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant promote demote: usage: olifant promote demote <challenge|validate> --reason \"<confirmed false block>\"")
		return 2
	}
	fs := flag.NewFlagSet("promote demote", flag.ExitOnError)
	statePath := fs.String("state", "", "promotion-state ledger path (default: ~/.olifant/promotion-state.yaml)")
	reason := fs.String("reason", "", "the confirmed false block — required (charter §7 demotion clause)")
	_ = fs.Parse(rest)

	path, err := promoteStatePath(*statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote: %v\n", err)
		return 2
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := promotion.Demote(path, surface, *reason, now); err != nil {
		fmt.Fprintf(os.Stderr, "olifant promote demote: %v\n", err)
		return 2
	}
	fmt.Printf("demoted %s -> advisory (reason: %s)\n", surface, *reason)
	return 0
}

// promotionBlockExit is the exit code a verdict surface returns when it is
// promoted to blocking and the live verdict is a hard block (charter §7,
// olifant#87). Distinct from 0 (clean/advisory) and 2 (usage error) so a
// wrapping hook can tell "the surface blocked you" from other failures.
const promotionBlockExit = 3

// enforceVerdict applies charter §7 promotion state to a completed verdict.
// If the surface is promoted to blocking and proceed is its hard-block value,
// it prints a blocking notice to stderr and returns promotionBlockExit;
// otherwise 0. noBlock forces advisory (for inspection). Any inability to read
// the state fails safe (advisory) — enforcement must never crash a run.
func enforceVerdict(surface, proceed string, noBlock, verbose bool) int {
	if noBlock {
		return 0
	}
	path, err := promotion.DefaultPath()
	if err != nil {
		return 0
	}
	return enforceVerdictAt(path, surface, proceed, verbose)
}

func enforceVerdictAt(path, surface, proceed string, verbose bool) int {
	st, err := promotion.Load(path)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "# warn: promotion state unreadable (%v) — advisory\n", err)
		}
		return 0
	}
	if st.HardBlocks(surface, proceed) {
		decision := ""
		if sf := st.Surfaces[surface]; sf != nil {
			decision = sf.Decision
		}
		fmt.Fprintf(os.Stderr,
			"# BLOCKED (charter §7, %s): %s returned a hard verdict (proceed=%s) and this surface is promoted to blocking. Address the verdict before proceeding — this is not advisory. (`olifant promote status` to inspect; -no-block to override.)\n",
			decision, surface, proceed)
		return promotionBlockExit
	}
	return 0
}

// leadingSurface pulls the surface positional (which must come first, before
// any flags) off the front of args, returning it plus the remaining flag args.
// ok is false when args is empty or starts with a flag.
func leadingSurface(args []string) (surface string, rest []string, ok bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, false
	}
	return args[0], args[1:], true
}

// splitCSV splits a comma-separated flag value, trimming spaces and dropping
// empties.
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
