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
	"github.com/ElatusDev/olifant/internal/digest"
	"github.com/ElatusDev/olifant/internal/promptgate"
	"github.com/ElatusDev/olifant/internal/shortterm"
	synthlib "github.com/ElatusDev/olifant/internal/synth"
)

// Digest implements `olifant digest <path>` (charter R6 v1, D-DG1..6): a
// cite-gated, SHA-cached local-model summary of one artifact. Cache hits
// need no stack; generation degrades with stack-up guidance.
func Digest(args []string) int {
	fs := flag.NewFlagSet("digest", flag.ExitOnError)
	refresh := fs.Bool("refresh", false, "regenerate even when a fresh cached digest exists")
	format := fs.String("format", "md", "output format: md|yaml")
	model := fs.String("model", "", "synthesizer model override")
	maxTokens := fs.Int("max-tokens", 900, "generation budget")
	timeoutSec := fs.Int("timeout", 300, "overall timeout in seconds")
	noRecord := fs.Bool("no-record", false, "do not write a short-term turn record")
	_ = fs.Parse(args)

	target := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if target == "" {
		fmt.Fprintln(os.Stderr, "olifant digest: missing path — usage: olifant digest <artifact-path>")
		return 2
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant digest:", err)
		return 2
	}

	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant digest: knowledge-base not found in cwd ancestors")
		return 2
	}
	kbRoot := filepath.Dir(found)
	platformRoot := filepath.Dir(kbRoot)

	sourceRel := abs
	if rel, rerr := filepath.Rel(platformRoot, abs); rerr == nil && !strings.HasPrefix(rel, "..") {
		sourceRel = filepath.ToSlash(rel)
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		fmt.Fprintln(os.Stderr, "olifant digest:", herr)
		return 1
	}
	cacheDir := filepath.Join(home, ".olifant", "digests")

	rt := config.Resolve()
	cfg := digest.Config{
		SourcePath: abs,
		SourceRel:  sourceRel,
		CacheDir:   cacheDir,
		Refresh:    *refresh,
		Model:      *model,
		MaxTokens:  *maxTokens,
	}

	// The cache path needs no stack and no resolver; probe/construct the
	// generation dependencies lazily so a cache hit works offline.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	start := time.Now()
	res, err := digest.Run(ctx, cfg)
	if err != nil && strings.Contains(err.Error(), "synthesizer and resolver are required") {
		// Cache miss → build the generation lane, then run for real.
		sc, defaultSynth, serr := synthlib.FromRuntime(rt)
		if serr != nil {
			fmt.Fprintln(os.Stderr, "olifant digest:", serr)
			return 1
		}
		if cfg.Model == "" {
			cfg.Model = defaultSynth
		}
		resolver, rerr := promptgate.NewResolver(platformRoot, kbRoot)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "olifant digest: resolver init failed (%v) — refusing to emit an unvalidated digest (D-DG2)\n", rerr)
			return 1
		}
		cfg.Synth = sc
		cfg.Resolver = resolver
		res, err = digest.Run(ctx, cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant digest: %v\n(stack down? see [[olifant-stack]]: Tailscale + chromadb port-forward; cached digests still serve offline)\n", err)
		return 1
	}

	if *format == "yaml" {
		out, merr := yaml.Marshal(map[string]any{
			"source": res.SourceRel, "source_sha": res.SourceSHA,
			"bytes_in": res.BytesIn, "bytes_out": res.BytesOut, "ratio": res.Ratio,
			"cache_hit": res.CacheHit, "digest": res.Digest,
		})
		if merr != nil {
			fmt.Fprintln(os.Stderr, "olifant digest: marshal:", merr)
			return 1
		}
		fmt.Print(string(out))
	} else {
		fmt.Print(res.Digest)
		if !strings.HasSuffix(res.Digest, "\n") {
			fmt.Println()
		}
	}

	prov := fmt.Sprintf("sha=%s", res.SourceSHA)
	if strings.HasPrefix(res.SourceRel, "knowledge-base/") {
		prov += " kb_checkout=" + kbCheckoutRef(kbRoot)
	}
	fmt.Fprintf(os.Stderr, "# elapsed=%s bytes=%d→%d ratio=%.1f%% cache_hit=%v attempts=%d %s\n",
		time.Since(start).Round(time.Millisecond), res.BytesIn, res.BytesOut, res.Ratio*100, res.CacheHit, res.Attempts, prov)

	if !*noRecord {
		ts := time.Now()
		rec := &shortterm.TurnRecord{
			TurnID:     shortterm.NewTurnID(ts, sourceRel),
			TS:         ts.UTC().Format(time.RFC3339),
			Subcommand: "digest",
			Request:    sourceRel,
			Digest: &shortterm.DigestBlock{
				Source: res.SourceRel, SourceSHA: res.SourceSHA,
				BytesIn: res.BytesIn, BytesOut: res.BytesOut, Ratio: res.Ratio,
				CacheHit: res.CacheHit, Attempts: res.Attempts, Model: cfg.Model,
			},
			Performance: shortterm.PerformanceBlock{ElapsedMs: time.Since(start).Milliseconds()},
		}
		if _, werr := shortterm.Write(kbRoot, rec); werr != nil {
			fmt.Fprintf(os.Stderr, "# warn: failed to write turn record: %v\n", werr)
		}
	}
	return 0
}
