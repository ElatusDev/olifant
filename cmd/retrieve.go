package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/config"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/prompt"
	"github.com/ElatusDev/olifant/internal/shortterm"
)

// adviceRuleDocTypes is the allow-list of corpus doc-types surfaced by
// `retrieve --file` advice: the rules + tech guides actionable at code-authoring
// time. Everything else (workflow, prompt, retro, template, skill, audit,
// view, memory…) is process/meta noise that out-competes the rules on distance.
// Filtering is done in Go over a larger fast pool rather than via a Chroma
// where-filter, which full-scans and adds ~14 s on these collections
// (olifant#106, P3 live finding).
var adviceRuleDocTypes = map[string]bool{
	"anti_pattern": true, "pattern": true, "standard": true, "decision": true,
	"doc": true, "architecture": true, "claude_md": true,
}

// File advice over-fetches (max of TopN×factor and advicePoolMin) before the Go
// rule-filter, because rule chunks rank low by distance against process docs —
// a small pool starves them (P3 live finding: pool 40 → 2 rules, pool 120 → 11).
const (
	advicePoolFactor = 5
	advicePoolMin    = 120
)

// adviceNoiseSourcePrefixes drop non-code "doc"-typed sources that the doc_type
// allow-list can't distinguish (memory snapshots, human usage docs, skill docs).
var adviceNoiseSourcePrefixes = []string{"claude-memory/", "for-you/", ".claude/"}

// codeSignal maps a code construct to a focused retrieval hint. A whole-file
// embedding is too diffuse to rank specific anti-patterns; a focused signal
// query surfaces them (P3 diagnosis). This is an advisory, extensible heuristic
// for the retrieval query — NOT the deterministic linter (that is T3, #105).
type codeSignal struct {
	re   *regexp.Regexp
	hint string
}

var codeSignals = []codeSignal{
	// Java / backend
	{regexp.MustCompile(`\bany\s*\(|\bany[A-Z]\w*\(`), "Mockito any() matcher argument matching"},
	{regexp.MustCompile(`\bfor\s*\(|\.forEach\(`), "manual loop instead of streams"},
	{regexp.MustCompile(`(?i)"\s*(select|insert|update|delete)\s|@Query`), "raw SQL native query bypassing the ORM"},
	{regexp.MustCompile(`System\.(out|err)\.`), "System.out logging instead of a logger"},
	{regexp.MustCompile(`catch\s*\([^)]*\)\s*\{\s*\}|except[^:]*:\s*pass`), "empty catch/except swallowed exception"},
	{regexp.MustCompile(`@Autowired`), "field injection instead of constructor injection"},
	{regexp.MustCompile(`(?i)\bpassword\b|\bsecret\b|api[_-]?key|token\s*=`), "hardcoded secret / credential handling"},
	{regexp.MustCompile(`==\s*null|!=\s*null|Optional\.get\(`), "null handling / Optional misuse"},
	// TypeScript / React / webapp
	{regexp.MustCompile(`console\.(log|error|warn)\(`), "console logging left in code"},
	{regexp.MustCompile(`\bvar\s|\bany\b\s*[:=]|as\s+any\b`), "loose typing (var / any) in TypeScript"},
	{regexp.MustCompile(`useEffect\(|useState\(`), "React hooks — effect/state dependency and setState pitfalls"},
	{regexp.MustCompile(`process\.env\.`), "process.env access (Vite import.meta.env)"},
	// Go
	{regexp.MustCompile(`fmt\.Print|panic\(`), "fmt.Print / panic instead of error handling + logging"},
	{regexp.MustCompile(`_\s*=\s*\w+|err\s*!=\s*nil`), "error handling — swallowed or unchecked errors"},
	// Python
	{regexp.MustCompile(`\bprint\(`), "print() instead of logging"},
}

// extractCodeSignals returns a focused query built from the code constructs
// present in body, or "" if none match (single-query fast path).
func extractCodeSignals(body string) string {
	var hints []string
	for _, s := range codeSignals {
		if s.re.MatchString(body) {
			hints = append(hints, s.hint)
		}
	}
	return strings.Join(hints, "; ")
}

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
	codeFile := fs.String("file", "", "code file to advise on: retrieval-only avoid/prefer/convention rules (pair-programming T1, olifant#106)")
	_ = fs.Parse(args)

	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		fmt.Fprintln(os.Stderr, "olifant retrieve: knowledge-base not found in cwd ancestors")
		return 2
	}
	kbRoot := filepath.Dir(found)
	platformRoot := filepath.Dir(kbRoot)

	// Two intake modes: an NL question, or a code file (--file) framed as a
	// compliance-review query for fast during-generation advice (olifant#106).
	// The --file path is retrieval-only (no synth, D-PP2) and read-only —
	// the input is a query, never a corpus source (AP184, D-PP6).
	var goal, displayQuery, codeBody string
	var families []string
	fileMode := *codeFile != ""
	if fileMode {
		body, rerr := os.ReadFile(*codeFile)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "olifant retrieve: read %s: %v\n", *codeFile, rerr)
			return 2
		}
		if strings.TrimSpace(string(body)) == "" {
			fmt.Fprintf(os.Stderr, "olifant retrieve: %s is empty — no advice to retrieve\n", *codeFile)
			return 0 // degrade, never error the caller (D-PP7)
		}
		// Query = the raw code (embed caps at a 3500-char head), NOT challenge's
		// compliance-review frame: the frame's boilerplate pulls retrieval toward
		// generic compliance prose, while the raw tokens align to specific rules
		// (P3 diagnosis — a keyword query surfaced the any() anti-pattern the
		// framed query missed).
		codeBody = string(body)
		goal = codeBody
		displayQuery = "code advice: " + *codeFile
		// Rule families only (D-PP3): anti-patterns/patterns/standards/guides
		// (corpus) + the "use X not Y" corrections (failure_modes) — NOT the
		// code/history families, whose raw source chunks crowd out rules (P3).
		// Process/meta doc-types are dropped from the corpus results by the Go
		// post-filter below (a Chroma where-filter is too slow, P3).
		families = []string{"corpus", "failure_modes"}
	} else {
		goal = strings.TrimSpace(strings.Join(fs.Args(), " "))
		if goal == "" {
			fmt.Fprintln(os.Stderr, `olifant retrieve: missing input — usage: olifant retrieve "<question>" | olifant retrieve --file <path>`)
			return 2
		}
		displayQuery = goal
	}

	var scopeList []string
	inferred := false
	switch {
	case *scopes != "":
		scopeList = strings.Split(*scopes, ",")
	case fileMode:
		// Scope from the code file's location; a tmp/scratch path outside the
		// platform tree yields nil → all scopes (caller passes -scope, D-PP5).
		if abs, err := filepath.Abs(*codeFile); err == nil {
			scopeList = inferScopes(filepath.Dir(abs), platformRoot)
			inferred = scopeList != nil
		}
	default:
		if cwd, err := os.Getwd(); err == nil {
			scopeList = inferScopes(cwd, platformRoot)
			inferred = scopeList != nil
		}
	}

	// Rules/anti-patterns live largely in the universal scope — union it in for
	// file advice so a stack-scoped file still surfaces cross-cutting rules
	// (nil scopeList = all scopes already includes universal). D-PP5.
	if fileMode && len(scopeList) > 0 {
		hasUniversal := false
		for _, s := range scopeList {
			if s == "universal" {
				hasUniversal = true
				break
			}
		}
		if !hasUniversal {
			scopeList = append(scopeList, "universal")
		}
	}

	// File advice over-fetches, then Go-filters to rule/guide chunks (P3).
	poolTopN := *topN
	if fileMode {
		if poolTopN = *topN * advicePoolFactor; poolTopN < advicePoolMin {
			poolTopN = advicePoolMin
		}
	}

	rt := config.Resolve()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	cfg := prompt.ContextConfig{
		Goal:      goal,
		OllamaURL: rt.OllamaURL,
		ChromaURL: rt.ChromaURL,
		Embedder:  rt.Embedder,
		Tenant:    rt.ChromaTenant,
		Database:  rt.ChromaDatabase,
		Scopes:    scopeList,
		TopN:      poolTopN,
		MaxChars:  *maxChars,
		Verbose:   *verbose,
		Families:  families,
	}

	// File advice may run a second focused query built from code signals — the
	// raw-code query surfaces patterns/conventions, but specific anti-patterns
	// need a focused query to rank (P3). The two are independent, so run them
	// concurrently (Chroma queries dominate; the mini serialises only embeds).
	// Clean files (no signals) keep the single-query path.
	var sig string
	if fileMode {
		sig = extractCodeSignals(codeBody)
	}

	start := time.Now()
	var res, res2 *prompt.ContextResult
	var err, err2 error
	if sig != "" {
		cfg2 := cfg
		cfg2.Goal = sig
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); res, err = prompt.BuildContext(ctx, cfg) }()
		go func() { defer wg.Done(); res2, err2 = prompt.BuildContext(ctx, cfg2) }()
		wg.Wait()
		if err == nil && err2 == nil {
			res.Chunks = append(res.Chunks, res2.Chunks...)
		} else if err == nil && err2 != nil && *verbose {
			fmt.Fprintf(os.Stderr, "# signal query skipped: %v\n", err2)
		}
	} else {
		res, err = prompt.BuildContext(ctx, cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "olifant retrieve: %v\n(stack down? see [[olifant-stack]]: Tailscale + chromadb port-forward; fall back to reading the docs directly)\n", err)
		return 1
	}

	if fileMode {
		res.Chunks = filterAdviceChunks(res.Chunks, *topN)
		res.Sources = chunkSources(res.Chunks)
	}

	var out []byte
	switch {
	case fileMode && *format == "md":
		out = []byte(renderAdviceMD(displayQuery, res))
	case fileMode:
		out, err = yaml.Marshal(groupAdvice(displayQuery, scopeList, res))
	case *format == "md":
		out = []byte(renderMD(goal, res))
	default:
		out, err = yaml.Marshal(contextOutput{Goal: goal, Scopes: scopeList, Chunks: res.Chunks})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant retrieve: marshal:", err)
		return 1
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
			TurnID:     shortterm.NewTurnID(ts, displayQuery),
			TS:         ts.UTC().Format(time.RFC3339),
			Subcommand: "retrieve",
			Scope:      scopeList,
			Request:    displayQuery,
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

// adviceOutput is the `retrieve --file` grouping: applicable rules bucketed for
// the code-authoring moment (olifant#106, D-PP4).
type adviceOutput struct {
	Query       string                `yaml:"query"`
	Scopes      []string              `yaml:"scopes"`
	Avoid       []prompt.ContextChunk `yaml:"avoid,omitempty"`
	Prefer      []prompt.ContextChunk `yaml:"prefer,omitempty"`
	Conventions []prompt.ContextChunk `yaml:"conventions,omitempty"`
}

// filterAdviceChunks keeps rule/guide corpus chunks + failure-mode corrections
// (dropping process/meta docs), balanced across the avoid/prefer/convention
// buckets so no bucket is starved by another that ranks higher globally — each
// gets up to keep/3+1 of its best (input is distance-sorted, so first-seen per
// bucket is best). Truncated to keep total (olifant#106, P3).
func filterAdviceChunks(chunks []prompt.ContextChunk, keep int) []prompt.ContextChunk {
	perBucket := keep/3 + 1
	seenSource := map[string]bool{}
	byBucket := map[string][]prompt.ContextChunk{}
	for _, c := range chunks {
		if adviceNoiseSource(c.Source) {
			continue
		}
		if !strings.HasSuffix(c.Scope, "/failure_modes") && !adviceRuleDocTypes[c.DocType] {
			continue
		}
		// One chunk per source across the whole result — favour diverse rules
		// over several sections of the same doc (input is distance-sorted, so
		// the first-seen chunk of a source is its most relevant).
		if seenSource[c.Source] {
			continue
		}
		b := adviceBucket(c)
		if len(byBucket[b]) >= perBucket {
			continue
		}
		seenSource[c.Source] = true
		byBucket[b] = append(byBucket[b], c)
	}
	out := make([]prompt.ContextChunk, 0, keep)
	for _, b := range []string{"avoid", "prefer", "convention"} {
		out = append(out, byBucket[b]...)
	}
	if len(out) > keep {
		out = out[:keep]
	}
	return out
}

// adviceNoiseSource reports whether a source is non-code "doc" noise.
func adviceNoiseSource(src string) bool {
	for _, p := range adviceNoiseSourcePrefixes {
		if strings.HasPrefix(src, p) {
			return true
		}
	}
	return false
}

// chunkSources returns the unique source paths of chunks, in order.
func chunkSources(chunks []prompt.ContextChunk) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range chunks {
		if c.Source == "" || seen[c.Source] {
			continue
		}
		seen[c.Source] = true
		out = append(out, c.Source)
	}
	return out
}

// adviceBucket classifies a retrieved chunk as something to avoid (anti-patterns
// / failure-mode corrections), prefer (proven patterns), or a convention/standard
// to honor. Family tag (Scope "<scope>/failure_modes") and doc_type are the keys;
// an AP-cite is the fallback signal.
func adviceBucket(c prompt.ContextChunk) string {
	if strings.HasSuffix(c.Scope, "/failure_modes") || c.DocType == "anti_pattern" {
		return "avoid"
	}
	if c.DocType == "pattern" {
		return "prefer"
	}
	for _, cite := range c.Cites {
		if strings.HasPrefix(cite, "AP") {
			return "avoid"
		}
	}
	return "convention"
}

// groupAdvice buckets the retrieved chunks into avoid/prefer/convention.
func groupAdvice(query string, scopes []string, res *prompt.ContextResult) adviceOutput {
	out := adviceOutput{Query: query, Scopes: scopes}
	for _, c := range res.Chunks {
		switch adviceBucket(c) {
		case "avoid":
			out.Avoid = append(out.Avoid, c)
		case "prefer":
			out.Prefer = append(out.Prefer, c)
		default:
			out.Conventions = append(out.Conventions, c)
		}
	}
	return out
}

// renderAdviceMD is the session-pasteable grouped rendering for `retrieve --file`.
func renderAdviceMD(query string, res *prompt.ContextResult) string {
	g := groupAdvice(query, nil, res)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", query)
	writeAdviceBucket(&b, "⛔ Avoid", g.Avoid)
	writeAdviceBucket(&b, "✅ Prefer", g.Prefer)
	writeAdviceBucket(&b, "📐 Conventions", g.Conventions)
	if len(res.Chunks) == 0 {
		b.WriteString("_No applicable rules retrieved._\n")
	}
	return b.String()
}

func writeAdviceBucket(b *strings.Builder, title string, chunks []prompt.ContextChunk) {
	if len(chunks) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	for _, c := range chunks {
		cites := ""
		if len(c.Cites) > 0 {
			cites = " · " + strings.Join(c.Cites, ", ")
		}
		fmt.Fprintf(b, "- **%s** (%s)%s\n  %s\n", c.Source, c.Scope, cites, c.Body)
	}
	b.WriteString("\n")
}
