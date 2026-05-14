package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/config"
)

// Challenge dispatches `olifant challenge "<user request>"`.
func Challenge(args []string) int {
	fs := flag.NewFlagSet("challenge", flag.ExitOnError)
	scopes := fs.String("scopes", "", "comma-separated scope filter (default: all)")
	topN := fs.Int("top", 8, "chunks to retrieve per scope")
	temp := fs.Float64("temperature", 0.1, "synthesizer temperature")
	maxTokens := fs.Int("max-tokens", 1024, "synthesizer num_predict")
	timeoutSec := fs.Int("timeout", 300, "overall timeout in seconds")
	verbose := fs.Bool("v", false, "verbose retrieval log")
	synth := fs.String("synth", "", "synthesizer model override")
	_ = fs.Parse(args)

	// User request: everything after flags, joined by spaces
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "olifant challenge: missing request — usage: olifant challenge \"<request>\"")
		return 2
	}
	request := strings.TrimSpace(strings.Join(rest, " "))
	if request == "" {
		fmt.Fprintln(os.Stderr, "olifant challenge: empty request")
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
		fmt.Println("config:", rt.String())
		fmt.Println("synth :", synthesizer)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	res, err := challenge.Run(ctx, challenge.Config{
		Request:     request,
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
		Verbose:     *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant challenge:", err)
		return 1
	}

	// Print metrics to stderr so stdout stays clean YAML
	fmt.Fprintf(os.Stderr, "# elapsed=%s embed=%dms retrieve=%dms synth=%dms eval_tokens=%d tokens/sec=%.1f retrieved=%d\n",
		res.Elapsed.Round(time.Millisecond), res.EmbedMs, res.RetrieveMs, res.SynthMs,
		res.SynthEvalCount, res.SynthTokensSec, res.RetrievedCount)
	// YAML goes to stdout
	fmt.Println(res.YAMLOutput)
	return 0
}
