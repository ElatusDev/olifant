package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/shortterm"
	"github.com/ElatusDev/olifant/internal/validate"
)

// Validate dispatches `olifant validate --claim <ref> --diff <ref>`.
//
// Claim and diff can each be supplied as:
//   - a file path (read from disk)
//   - --claim-text "literal text"
//   - --diff <git ref> (e.g., HEAD~1..HEAD or a commit SHA — runs git diff)
func Validate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	claimFile := fs.String("claim", "", "path to file containing Claude's claim (alternative: --claim-text)")
	claimText := fs.String("claim-text", "", "literal claim text (alternative to --claim)")
	diffRef := fs.String("diff", "", "git revision range (HEAD~1..HEAD), single SHA, or path to a patch file")
	repoCwd := fs.String("repo", ".", "git repo working directory for `git diff` invocations")
	scopes := fs.String("scopes", "", "comma-separated scope filter (informational only — turn metadata)")
	maxTokens := fs.Int("max-tokens", 1024, "synthesizer num_predict")
	temp := fs.Float64("temperature", 0, "synthesizer temperature")
	timeoutSec := fs.Int("timeout", 300, "overall timeout seconds")
	verbose := fs.Bool("v", false, "verbose log")
	synth := fs.String("synth", "", "synthesizer model override")
	noRecord := fs.Bool("no-record", false, "skip short-term turn record")
	_ = fs.Parse(args)

	// Resolve claim text.
	claim := strings.TrimSpace(*claimText)
	if claim == "" && *claimFile != "" {
		body, err := os.ReadFile(*claimFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "validate: read claim file %s: %v\n", *claimFile, err)
			return 1
		}
		claim = strings.TrimSpace(string(body))
	}
	if claim == "" {
		fmt.Fprintln(os.Stderr, "validate: claim is required — pass --claim <file> or --claim-text \"…\"")
		return 2
	}

	// Resolve diff.
	if *diffRef == "" {
		fmt.Fprintln(os.Stderr, "validate: --diff is required (git ref OR path to patch file)")
		return 2
	}
	repoAbs, _ := filepath.Abs(*repoCwd)
	diffBody, derr := validate.ResolveDiff(*diffRef, repoAbs)
	if derr != nil {
		fmt.Fprintln(os.Stderr, "validate:", derr)
		return 1
	}
	if strings.TrimSpace(diffBody) == "" {
		fmt.Fprintln(os.Stderr, "validate: diff is empty — nothing to validate against")
		return 1
	}

	rt := config.Resolve()
	synthesizer := rt.Synthesizer
	if *synth != "" {
		synthesizer = *synth
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "validate: claim=%d chars, diff=%d chars, synth=%s\n",
			len(claim), len(diffBody), synthesizer)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	res, err := validate.Run(ctx, validate.Config{
		Claim:       claim,
		Diff:        diffBody,
		OllamaURL:   rt.OllamaURL,
		Synthesizer: synthesizer,
		Temperature: *temp,
		MaxTokens:   *maxTokens,
		Verbose:     *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return 1
	}

	verdict, proceed := res.ExtractVerdict()
	fmt.Fprintf(os.Stderr,
		"# elapsed=%s synth=%dms eval_tokens=%d tokens/sec=%.1f verdict=%s proceed=%s\n",
		res.Elapsed.Round(time.Millisecond), res.SynthMs,
		res.SynthEvalCount, res.SynthTokensSec, verdict, proceed)
	fmt.Println(res.YAMLOutput)

	// Short-term turn record.
	if !*noRecord {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			kbRoot := filepath.Dir(found)
			ts := time.Now()
			var scopeList []string
			if *scopes != "" {
				for _, s := range strings.Split(*scopes, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						scopeList = append(scopeList, s)
					}
				}
			}
			rec := &shortterm.TurnRecord{
				TurnID:     shortterm.NewTurnID(ts, claim),
				TS:         ts.UTC().Format(time.RFC3339),
				Subcommand: "validate",
				Scope:      scopeList,
				Request:    "claim: " + truncate(claim, 240) + " | diff: " + truncate(diffBody, 240),
				Validate: &shortterm.ValidateBlock{
					Verdict: verdict,
				},
				Performance: shortterm.PerformanceBlock{
					ElapsedMs:    res.Elapsed.Milliseconds(),
					SynthMs:      res.SynthMs,
					EvalTokens:   res.SynthEvalCount,
					TokensPerSec: res.SynthTokensSec,
				},
			}
			if path, werr := shortterm.Write(kbRoot, rec); werr != nil {
				fmt.Fprintf(os.Stderr, "# warn: failed to write turn record: %v\n", werr)
			} else if *verbose {
				fmt.Fprintf(os.Stderr, "# turn recorded: %s\n", path)
			}
		}
	}

	// Exit code reflects verdict.
	switch verdict {
	case "failed":
		return 2
	case "partial":
		return 1
	default:
		return 0
	}
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
