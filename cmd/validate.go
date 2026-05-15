package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
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
//
// When a knowledge-base/ root is discoverable from cwd, the validator
// loads a CiteValidator and enables ChromaDB-grounded retrieval + per-claim
// retry on weak assessments. Without a KB root, the pipeline degrades to
// the legacy single-synth path.
func Validate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	claimFile := fs.String("claim", "", "path to file containing Claude's claim (alternative: --claim-text)")
	claimText := fs.String("claim-text", "", "literal claim text (alternative to --claim)")
	diffRef := fs.String("diff", "", "git revision range (HEAD~1..HEAD), single SHA, or path to a patch file")
	repoCwd := fs.String("repo", ".", "git repo working directory for `git diff` invocations")
	scopes := fs.String("scopes", "", "comma-separated scope filter for retrieval (e.g., backend,webapp); empty = default 7-scope union")
	maxTokens := fs.Int("max-tokens", 4096, "synthesizer num_predict")
	topN := fs.Int("top-n", 8, "retrieved chunks per validate call")
	temp := fs.Float64("temperature", 0, "synthesizer temperature")
	timeoutSec := fs.Int("timeout", 300, "overall timeout seconds")
	retries := fs.Int("retries", -1, "additional synth attempts on weak assessments; -1 = auto (1 when validator wired)")
	verbose := fs.Bool("v", false, "verbose log")
	synth := fs.String("synth", "", "synthesizer model override")
	noRetrieval := fs.Bool("no-retrieval", false, "skip ChromaDB retrieval and run the legacy single-synth path")
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

	// Scope parsing
	var scopeList []string
	if *scopes != "" {
		for _, s := range strings.Split(*scopes, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				scopeList = append(scopeList, s)
			}
		}
	}

	// Load CiteValidator if a KB root is reachable (and the user didn't disable retrieval).
	var citeValidator *challenge.CiteValidator
	if !*noRetrieval {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			kbRoot := filepath.Dir(found)
			platformRoot := filepath.Dir(kbRoot)
			v, verr := challenge.NewCiteValidator(platformRoot, kbRoot)
			if verr == nil {
				citeValidator = v
				if *verbose {
					fmt.Fprintf(os.Stderr, "validator: %d terms loaded (dict=%d concepts=%d constraints=%d glossary=%d)\n",
						v.KnownCount(),
						v.CountByLayer(challenge.LayerDictionary),
						v.CountByLayer(challenge.LayerConcept),
						v.CountByLayer(challenge.LayerConstraint),
						v.CountByLayer(challenge.LayerGlossary))
				}
			} else if *verbose {
				fmt.Fprintf(os.Stderr, "validate: cite-validator init failed (%v) — proceeding without\n", verr)
			}
		}
	}

	if *verbose {
		mode := "grounded"
		if citeValidator == nil {
			mode = "legacy-single-synth"
		}
		fmt.Fprintf(os.Stderr, "validate: claim=%d chars, diff=%d chars, synth=%s, mode=%s\n",
			len(claim), len(diffBody), synthesizer, mode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	res, err := validate.Run(ctx, validate.Config{
		Claim:              claim,
		Diff:               diffBody,
		OllamaURL:          rt.OllamaURL,
		ChromaURL:          rt.ChromaURL,
		Embedder:           rt.Embedder,
		Synthesizer:        synthesizer,
		Tenant:             rt.ChromaTenant,
		Database:           rt.ChromaDatabase,
		Scopes:             scopeList,
		TopN:               *topN,
		Temperature:        *temp,
		MaxTokens:          *maxTokens,
		Verbose:            *verbose,
		Validator:          citeValidator,
		MaxValidateRetries: *retries,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return 1
	}

	verdict, proceed := res.ExtractVerdict()
	fmt.Fprintf(os.Stderr,
		"# elapsed=%s embed=%dms retrieve=%dms synth=%dms attempts=%d eval_tokens=%d tokens/sec=%.1f verdict=%s proceed=%s\n",
		res.Elapsed.Round(time.Millisecond), res.EmbedMs, res.RetrieveMs, res.SynthMs,
		res.ValidateAttempts, res.SynthEvalCount, res.SynthTokensSec, verdict, proceed)
	if *verbose && len(res.RemainingViolations) > 0 {
		fmt.Fprintf(os.Stderr, "# remaining violations after final attempt: %d\n", len(res.RemainingViolations))
		for _, v := range res.RemainingViolations {
			fmt.Fprintf(os.Stderr, "  [%s] [%s] %s @ %s\n", v.Severity.String(), v.Code, v.Note, v.Location)
		}
	}
	fmt.Println(res.YAMLOutput)

	// Short-term turn record.
	if !*noRecord {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			kbRoot := filepath.Dir(found)
			ts := time.Now()
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
