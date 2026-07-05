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
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/prompt"
	"github.com/ElatusDev/olifant/internal/shortterm"
)

// repoDirAliases normalizes on-disk directory names to the repo names
// corpus.ScopeForRepoClaudeMd knows (symlink targets differ from repo names).
var repoDirAliases = map[string]string{
	"platform-core-api":  "core-api",
	"AkademiaPlusWebApp": "akademia-plus-web",
	"elatusdev-infra":    "infra",
}

// inferScopes maps cwd → the repo's scope (+ universal). Explicit -scope wins
// at the caller; unknown locations return nil (→ all scopes, D-RV2).
func inferScopes(cwd, platformRoot string) []string {
	rel, err := filepath.Rel(platformRoot, cwd)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return nil
	}
	repoDir := strings.Split(filepath.ToSlash(rel), "/")[0]
	if alias, ok := repoDirAliases[repoDir]; ok {
		repoDir = alias
	}
	scope := corpus.ScopeForRepoClaudeMd(repoDir)
	if scope == "" {
		return nil
	}
	if scope == "universal" {
		return []string{"universal"}
	}
	return []string{scope, "universal"}
}

// retrieveEconomy sums the on-disk sizes of the distinct KB source docs the
// chunks came from — the bytes a session would otherwise read wholesale.
// Repo-chunk provenance (repo@sha:path) is skipped: not a local file.
func retrieveEconomy(kbRoot string, sources []string) int64 {
	var total int64
	for _, src := range sources {
		if strings.ContainsAny(src, "@") {
			continue
		}
		if st, err := os.Stat(filepath.Join(kbRoot, filepath.FromSlash(src))); err == nil {
			total += st.Size()
		}
	}
	return total
}

// Retrieve implements `olifant retrieve "<question>"` (charter R5) — the
// general retrieval-instead-of-reading interface: top-N scoped, cite-tagged
// KB chunks for any question. Thin over prompt.BuildContext (D-RV1); never
// calls a synthesizer.
func Retrieve(args []string) int {
	fs := flag.NewFlagSet("retrieve", flag.ExitOnError)
	scopes := fs.String("scope", "", "comma-separated scope filter (default: inferred from cwd, else all)")
	topN := fs.Int("top", 8, "chunks to retrieve globally after distance sort")
	maxChars := fs.Int("max-chars", 1200, "per-chunk body cap (0 = uncapped)")
	format := fs.String("format", "yaml", "output format: yaml|md")
	timeoutSec := fs.Int("timeout", 60, "overall timeout in seconds")
	verbose := fs.Bool("v", false, "verbose retrieval log")
	noRecord := fs.Bool("no-record", false, "do not write a short-term turn record")
	_ = fs.Parse(args)

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `olifant retrieve: missing question — usage: olifant retrieve "<question>"`)
		return 2
	}

	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant retrieve: knowledge-base not found in cwd ancestors")
		return 2
	}
	kbRoot := filepath.Dir(found)
	platformRoot := filepath.Dir(kbRoot)

	var scopeList []string
	inferred := false
	if *scopes != "" {
		scopeList = strings.Split(*scopes, ",")
	} else if cwd, err := os.Getwd(); err == nil {
		scopeList = inferScopes(cwd, platformRoot)
		inferred = scopeList != nil
	}

	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	start := time.Now()
	res, err := prompt.BuildContext(ctx, prompt.ContextConfig{
		Goal:      question,
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
		fmt.Fprintf(os.Stderr, "olifant retrieve: %v\n(stack down? see [[olifant-stack]]: Tailscale + chromadb port-forward; fall back to reading the docs directly)\n", err)
		return 1
	}

	var out []byte
	if *format == "md" {
		out = []byte(renderMD(question, res))
	} else {
		out, err = yaml.Marshal(contextOutput{Goal: question, Scopes: scopeList, Chunks: res.Chunks})
		if err != nil {
			fmt.Fprintln(os.Stderr, "olifant retrieve: marshal:", err)
			return 1
		}
	}
	fmt.Print(string(out))

	sourceBytes := retrieveEconomy(kbRoot, res.Sources)
	scopeNote := strings.Join(scopeList, ",")
	if scopeNote == "" {
		scopeNote = "all"
	}
	if inferred {
		scopeNote += " (inferred)"
	}
	economy := fmt.Sprintf("%dB payload vs %dB sources", len(out), sourceBytes)
	if sourceBytes == 0 {
		// All sources were repo/memory chunks — not locally measurable files.
		economy = fmt.Sprintf("%dB payload (sources not locally measurable)", len(out))
	}
	fmt.Fprintf(os.Stderr, "# elapsed=%s scopes=%s retrieved=%d economy=%s\n",
		time.Since(start).Round(time.Millisecond), scopeNote, len(res.Chunks), economy)

	if !*noRecord {
		ts := time.Now()
		rec := &shortterm.TurnRecord{
			TurnID:     shortterm.NewTurnID(ts, question),
			TS:         ts.UTC().Format(time.RFC3339),
			Subcommand: "retrieve",
			Scope:      scopeList,
			Request:    question,
			Retrieve: &shortterm.RetrieveBlock{
				Inferred:       inferred,
				RetrievedCount: len(res.Chunks),
				Sources:        res.Sources,
				PayloadBytes:   len(out),
				SourceBytes:    sourceBytes,
			},
			Performance: shortterm.PerformanceBlock{
				ElapsedMs:  time.Since(start).Milliseconds(),
				EmbedMs:    res.EmbedMs,
				RetrieveMs: res.RetrieveMs,
			},
		}
		if path, werr := shortterm.Write(kbRoot, rec); werr != nil {
			fmt.Fprintf(os.Stderr, "# warn: failed to write turn record: %v\n", werr)
		} else if *verbose {
			fmt.Fprintf(os.Stderr, "# turn recorded: %s\n", path)
		}
	}
	return 0
}

// renderMD is the compact session-pasteable rendering (D-RV4).
func renderMD(question string, res *prompt.ContextResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## KB retrieval: %s\n\n", question)
	for i, c := range res.Chunks {
		cites := ""
		if len(c.Cites) > 0 {
			cites = " · cites: " + strings.Join(c.Cites, ", ")
		}
		fmt.Fprintf(&b, "### %d. %s (%s, d=%.3f)%s\n\n%s\n\n", i+1, c.Source, c.Scope, c.Distance, cites, c.Body)
	}
	return b.String()
}
