package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/promptgate"
	"github.com/ElatusDev/olifant/internal/shortterm"
)

// promptCheck implements `olifant prompt check <doc.md>` — the deterministic
// cite gate on a generated prompt/workflow document (charter R2). Offline:
// needs only the filesystem (live KB sources + optional corpus manifest); no
// Chroma, no Ollama, no LLM. Exit 0 = pass, 1 = unresolved cites (advisory —
// the calling skill renders, it does not block), 2 = usage/setup error.
func promptCheck(args []string) int {
	fs := flag.NewFlagSet("prompt check", flag.ExitOnError)
	verbose := fs.Bool("v", false, "list resolved cites too, not just failures")
	noRecord := fs.Bool("no-record", false, "do not write a short-term turn record")
	kbRootFlag := fs.String("kb-root", "", "resolve bare IDs + KB path cites against this KB tree (default: OLIFANT_KB_ROOT, then findUp) — pin to a branch worktree when the doc cites artifacts not yet on the shared checkout (olifant#79)")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "olifant prompt check: usage: olifant prompt check [-kb-root <tree>] <doc.md>")
		return 2
	}
	docPath := fs.Arg(0)

	// Same tree-pinning lineage as the eval gate (D224/D227): the pin moves
	// kbRoot only — repo path cites keep resolving against the REAL platform
	// root from findUp (a pinned worktree's parent is not the platform).
	kbRoot, platformRoot := resolveRoots(*kbRootFlag)
	if kbRoot == "" {
		fmt.Fprintln(os.Stderr, "olifant prompt check: knowledge-base not found in cwd ancestors (or pass -kb-root)")
		return 2
	}

	start := time.Now()
	resolver, err := promptgate.NewResolver(platformRoot, kbRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt check:", err)
		return 2
	}
	report, err := resolver.CheckDoc(docPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt check:", err)
		return 2
	}

	shown := *report
	if !*verbose {
		kept := make([]promptgate.CitedItem, 0, len(report.Items))
		for _, it := range report.Items {
			if it.Verdict != promptgate.VerdictResolved {
				kept = append(kept, it)
			}
		}
		shown.Items = kept
	}
	out, mErr := yaml.Marshal(&shown)
	if mErr != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt check: marshal:", mErr)
		return 2
	}
	fmt.Print(string(out))
	// The Layer-1 scan reads the KB WORKING TREE — a stale checkout yields
	// false unresolveds, so surface which ref was actually judged against.
	fmt.Fprintf(os.Stderr, "# elapsed=%s resolved=%d stale=%d unresolved=%d known_artifacts=%d kb_checkout=%s\n",
		time.Since(start).Round(time.Millisecond),
		report.Resolved, report.Stale, report.Unresolved, resolver.KnownArtifactCount(),
		kbCheckoutRef(kbRoot))

	if !*noRecord {
		verdict := "pass"
		if !report.Pass {
			verdict = "fail"
		}
		var unresolvedCites []string
		for _, it := range report.Items {
			if it.Verdict == promptgate.VerdictUnresolved {
				unresolvedCites = append(unresolvedCites, it.Cite)
			}
		}
		ts := time.Now()
		rec := &shortterm.TurnRecord{
			TurnID:     shortterm.NewTurnID(ts, docPath),
			TS:         ts.UTC().Format(time.RFC3339),
			Subcommand: "prompt check",
			Request:    docPath,
			PromptCheck: &shortterm.PromptCheckBlock{
				DocPath:         docPath,
				Verdict:         verdict,
				Resolved:        report.Resolved,
				Stale:           report.Stale,
				Unresolved:      report.Unresolved,
				UnresolvedCites: unresolvedCites,
			},
			Performance: shortterm.PerformanceBlock{
				ElapsedMs: time.Since(start).Milliseconds(),
			},
		}
		if path, werr := shortterm.Write(kbRoot, rec); werr != nil {
			fmt.Fprintf(os.Stderr, "# warn: failed to write turn record: %v\n", werr)
		} else if *verbose {
			fmt.Fprintf(os.Stderr, "# turn recorded: %s\n", path)
		}
	}

	if !report.Pass {
		return 1
	}
	return 0
}

// kbCheckoutRef labels the KB working tree being judged against ("branch@sha"
// or "not-a-git-checkout") so a stale checkout is visible in the summary.
func kbCheckoutRef(kbRoot string) string {
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = kbRoot
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	sha := run("rev-parse", "--short", "HEAD")
	if sha == "" {
		return "not-a-git-checkout"
	}
	branch := run("branch", "--show-current")
	if branch == "" {
		branch = "detached"
	}
	return branch + "@" + sha
}
