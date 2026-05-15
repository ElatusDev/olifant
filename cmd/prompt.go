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
	"github.com/ElatusDev/olifant/internal/prompt"
	"github.com/ElatusDev/olifant/internal/shortterm"
)

// Prompt dispatches `olifant prompt <build|...>`.
func Prompt(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant prompt: missing action (build)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "build":
		return promptBuild(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant prompt: unknown action %q\n", action)
		return 2
	}
}

func promptBuild(args []string) int {
	fs := flag.NewFlagSet("prompt build", flag.ExitOnError)
	scopes := fs.String("scope", "", "comma-separated scope filter (default: all)")
	topN := fs.Int("top", 8, "chunks to retrieve globally after distance sort")
	temp := fs.Float64("temperature", 0.1, "synthesizer temperature")
	maxTokens := fs.Int("max-tokens", 1024, "synthesizer num_predict")
	timeoutSec := fs.Int("timeout", 600, "overall timeout in seconds")
	verbose := fs.Bool("v", false, "verbose retrieval + synth log")
	synth := fs.String("synth", "", "synthesizer model override")
	out := fs.String("out", "plans", "output directory for plans/<plan_id>.yaml")
	noRecord := fs.Bool("no-record", false, "do not write a short-term turn record")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `olifant prompt build: missing goal — usage: olifant prompt build "<goal>"`)
		return 2
	}
	goal := strings.TrimSpace(strings.Join(rest, " "))
	if goal == "" {
		fmt.Fprintln(os.Stderr, "olifant prompt build: empty goal")
		return 2
	}

	rt := config.Resolve()
	synthesizer := rt.Synthesizer
	if *synth != "" {
		synthesizer = *synth
	}

	var scopeList []string
	if *scopes != "" {
		for _, s := range strings.Split(*scopes, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				scopeList = append(scopeList, s)
			}
		}
	}

	if *verbose {
		fmt.Fprintln(os.Stderr, "config:", rt.String())
		fmt.Fprintln(os.Stderr, "synth :", synthesizer)
		fmt.Fprintln(os.Stderr, "goal  :", goal)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	res, err := prompt.Build(ctx, prompt.Config{
		Goal:        goal,
		OllamaURL:   rt.OllamaURL,
		ChromaURL:   rt.ChromaURL,
		Embedder:    rt.Embedder,
		Synthesizer: synthesizer,
		Tenant:      rt.ChromaTenant,
		Database:    rt.ChromaDatabase,
		Scopes:      scopeList,
		TopN:        *topN,
		Temperature: *temp,
		MaxTokens:   *maxTokens,
		OutDir:      *out,
		Verbose:     *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt build:", err)
		return 1
	}

	fmt.Fprintf(os.Stderr,
		"# elapsed=%s embed=%dms retrieve=%dms synth=%dms eval_tokens=%d tokens/sec=%.1f retrieved=%d steps=%d split=%v warnings=%d\n",
		res.Elapsed.Round(time.Millisecond), res.EmbedMs, res.RetrieveMs, res.SynthMs,
		res.SynthEvalCount, res.SynthTokensSec, res.RetrievedCount, res.StepCount, res.Split, len(res.Warnings))
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "# WARN:", w)
	}

	// Plan path(s) go to stdout.
	if res.Split {
		for _, p := range res.SubPlanPaths {
			fmt.Println(p)
		}
	} else {
		fmt.Println(res.PlanPath)
	}

	if !*noRecord {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			kbRoot := filepath.Dir(found)
			ts := time.Now()
			rec := &shortterm.TurnRecord{
				TurnID:     shortterm.NewTurnID(ts, goal),
				TS:         ts.UTC().Format(time.RFC3339),
				Subcommand: "prompt build",
				Scope:      scopeList,
				Request:    goal,
				PromptBuild: &shortterm.PromptBuildBlock{
					OutputPath:     res.PlanPath,
					SignalsEmitted: res.RetrievedSources,
					PayloadBytes:   len(res.RawSynthJSON),
				},
				Performance: shortterm.PerformanceBlock{
					ElapsedMs:    res.Elapsed.Milliseconds(),
					EmbedMs:      res.EmbedMs,
					RetrieveMs:   res.RetrieveMs,
					SynthMs:      res.SynthMs,
					EvalTokens:   res.SynthEvalCount,
					TokensPerSec: res.SynthTokensSec,
				},
			}
			path, werr := shortterm.Write(kbRoot, rec)
			if werr != nil {
				fmt.Fprintf(os.Stderr, "# warn: failed to write turn record: %v\n", werr)
			} else if *verbose {
				fmt.Fprintf(os.Stderr, "# turn recorded: %s\n", path)
			}
		}
	}
	return 0
}
