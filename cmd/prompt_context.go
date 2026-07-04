package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/prompt"
	"github.com/ElatusDev/olifant/internal/shortterm"
)

// contextOutput is the stdout YAML shape consumed by the /olifant-prompt skill.
type contextOutput struct {
	Goal   string                `yaml:"goal"`
	Scopes []string              `yaml:"scopes,omitempty"`
	Chunks []prompt.ContextChunk `yaml:"chunks"`
}

// promptContext implements `olifant prompt context "<goal>"` — the retrieval-
// only grounding feed for /prompt (charter R2, D-OP1: no synthesizer).
func promptContext(args []string) int {
	fs := flag.NewFlagSet("prompt context", flag.ExitOnError)
	scopes := fs.String("scope", "", "comma-separated scope filter (default: all)")
	topN := fs.Int("top", 8, "chunks to retrieve globally after distance sort")
	maxChars := fs.Int("max-chars", 1200, "per-chunk body cap in the output (0 = uncapped)")
	timeoutSec := fs.Int("timeout", 60, "overall timeout in seconds")
	verbose := fs.Bool("v", false, "verbose retrieval log")
	noRecord := fs.Bool("no-record", false, "do not write a short-term turn record")
	_ = fs.Parse(args)

	goal := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if goal == "" {
		fmt.Fprintln(os.Stderr, `olifant prompt context: missing goal — usage: olifant prompt context "<goal>"`)
		return 2
	}
	var scopeList []string
	if *scopes != "" {
		scopeList = strings.Split(*scopes, ",")
	}

	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	start := time.Now()
	res, err := prompt.BuildContext(ctx, prompt.ContextConfig{
		Goal:      goal,
		OllamaURL: rt.OllamaURL,
		ChromaURL: rt.ChromaURL,
		Embedder:  rt.Embedder,
		Tenant:    rt.ChromaTenant,
		Database:  rt.ChromaDatabase,
		Scopes:    scopeList,
		TopN:      *topN,
		MaxChars:  *maxChars,
		Verbose:   *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt context:", err)
		return 1
	}

	out, mErr := yaml.Marshal(contextOutput{Goal: goal, Scopes: scopeList, Chunks: res.Chunks})
	if mErr != nil {
		fmt.Fprintln(os.Stderr, "olifant prompt context: marshal:", mErr)
		return 1
	}
	fmt.Print(string(out))
	fmt.Fprintf(os.Stderr, "# elapsed=%s embed=%dms retrieve=%dms retrieved=%d payload_bytes=%d\n",
		time.Since(start).Round(time.Millisecond), res.EmbedMs, res.RetrieveMs, len(res.Chunks), len(out))

	if !*noRecord {
		if found, ok := findUp("knowledge-base/README.md"); ok {
			kbRoot := filepath.Dir(found)
			ts := time.Now()
			rec := &shortterm.TurnRecord{
				TurnID:     shortterm.NewTurnID(ts, goal),
				TS:         ts.UTC().Format(time.RFC3339),
				Subcommand: "prompt context",
				Scope:      scopeList,
				Request:    goal,
				PromptContext: &shortterm.PromptContextBlock{
					RetrievedCount: len(res.Chunks),
					Sources:        res.Sources,
					PayloadBytes:   len(out),
				},
				Performance: shortterm.PerformanceBlock{
					ElapsedMs:  time.Since(start).Milliseconds(),
					EmbedMs:    res.EmbedMs,
					RetrieveMs: res.RetrieveMs,
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
