// Package advice implements the T1 pair-programming retrieval lane — the logic
// behind `olifant retrieve --file` (olifant#106, D269): given a code snippet, it
// returns the applicable KB rules grouped avoid/prefer/convention, retrieval-only
// (never a synthesizer). Extracted from cmd/retrieve.go so the eval scorer
// (internal/eval, advice-quality-v1) can exercise the same pipeline the CLI does
// (olifant#110).
package advice

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/ElatusDev/olifant/internal/prompt"
)

// Config parameterises Run. Scopes are the already-resolved retrieval scopes
// (the caller unions "universal" — where cross-cutting rules live); Run does the
// advice-specific over-fetch, dual-query, filter and bucketing.
type Config struct {
	CodeBody  string
	Scopes    []string
	OllamaURL string
	ChromaURL string
	Embedder  string
	Tenant    string
	Database  string
	TopN      int // final per-bucket-balanced result size (default 8)
	MaxChars  int
	Verbose   bool
}

// Result is the grouped advice plus the flat filtered chunks (for economy /
// counts) and retrieval timings.
type Result struct {
	Avoid       []prompt.ContextChunk
	Prefer      []prompt.ContextChunk
	Conventions []prompt.ContextChunk
	Chunks      []prompt.ContextChunk // flat, post-filter (distance order)
	Sources     []string
	EmbedMs     int64
	RetrieveMs  int64
}

// ruleFamilies are the only families the advice lane queries: KB rules/guides
// (corpus) + the "use X not Y" corrections (failure_modes) — NOT the code/history
// families, whose raw source chunks crowd out rules (D-PP3).
var ruleFamilies = []string{"corpus", "failure_modes"}

// advicePoolFactor / advicePoolMin: over-fetch before the Go rule-filter, because
// rule chunks rank low by distance against process docs — a small pool starves
// them (P3 live finding: pool 40 → 2 rules, pool 120 → 11).
const (
	advicePoolFactor = 5
	advicePoolMin    = 120
)

// Run executes the T1 advice pipeline: raw-code query + (when code signals are
// present) a concurrent focused signal query, merged, filtered to rule/guide
// chunks and bucket-balanced. Retrieval-only — never calls a synthesizer.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	topN := cfg.TopN
	if topN <= 0 {
		topN = 8
	}
	poolTopN := topN * advicePoolFactor
	if poolTopN < advicePoolMin {
		poolTopN = advicePoolMin
	}

	base := prompt.ContextConfig{
		Goal:      cfg.CodeBody, // raw code, not a compliance frame (P3)
		OllamaURL: cfg.OllamaURL,
		ChromaURL: cfg.ChromaURL,
		Embedder:  cfg.Embedder,
		Tenant:    cfg.Tenant,
		Database:  cfg.Database,
		Scopes:    cfg.Scopes,
		TopN:      poolTopN,
		MaxChars:  cfg.MaxChars,
		Verbose:   cfg.Verbose,
		Families:  ruleFamilies,
	}

	// A whole-file embedding is too diffuse to rank specific anti-patterns; a
	// focused signal query surfaces them (P3). Run the two independent queries
	// concurrently. Clean files (no signals) keep the single-query path.
	sig := ExtractCodeSignals(cfg.CodeBody)
	var res, res2 *prompt.ContextResult
	var err, err2 error
	if sig != "" {
		sigCfg := base
		sigCfg.Goal = sig
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); res, err = prompt.BuildContext(ctx, base) }()
		go func() { defer wg.Done(); res2, err2 = prompt.BuildContext(ctx, sigCfg) }()
		wg.Wait()
		if err == nil && err2 == nil {
			res.Chunks = append(res.Chunks, res2.Chunks...)
		}
	} else {
		res, err = prompt.BuildContext(ctx, base)
	}
	if err != nil {
		return nil, err
	}

	chunks := filterAdviceChunks(res.Chunks, topN)
	out := &Result{
		Chunks:     chunks,
		Sources:    chunkSources(chunks),
		EmbedMs:    res.EmbedMs,
		RetrieveMs: res.RetrieveMs,
	}
	for _, c := range chunks {
		switch Bucket(c) {
		case "avoid":
			out.Avoid = append(out.Avoid, c)
		case "prefer":
			out.Prefer = append(out.Prefer, c)
		default:
			out.Conventions = append(out.Conventions, c)
		}
	}
	return out, nil
}

// Cites returns the union of cite ids across all chunks in the given bucket
// ("avoid"|"prefer"|"convention"), for the eval scorer (olifant#110).
func (r *Result) Cites(bucket string) []string {
	var src []prompt.ContextChunk
	switch bucket {
	case "avoid":
		src = r.Avoid
	case "prefer":
		src = r.Prefer
	case "convention":
		src = r.Conventions
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range src {
		for _, cite := range c.Cites {
			if !seen[cite] {
				seen[cite] = true
				out = append(out, cite)
			}
		}
	}
	return out
}

// --- retrieval curation (moved verbatim from cmd/retrieve.go, olifant#106) ---

// adviceRuleDocTypes is the allow-list of corpus doc-types surfaced as advice:
// the rules + tech guides actionable at code-authoring time. Everything else
// (workflow, prompt, retro, template, skill, audit, view, memory…) is process/
// meta noise that out-competes the rules on distance.
var adviceRuleDocTypes = map[string]bool{
	"anti_pattern": true, "pattern": true, "standard": true, "decision": true,
	"doc": true, "architecture": true, "claude_md": true,
}

// adviceNoiseSourcePrefixes drop non-code "doc"-typed sources the doc_type
// allow-list can't distinguish (memory snapshots, human usage docs, skill docs).
var adviceNoiseSourcePrefixes = []string{"claude-memory/", "for-you/", ".claude/"}

// codeSignal maps a code construct to a focused retrieval hint. Advisory,
// extensible heuristic for the retrieval query — NOT the deterministic linter
// (that is T3, closed not-planned, D270).
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

// ExtractCodeSignals returns a focused query built from the code constructs
// present in body, or "" if none match (single-query fast path).
func ExtractCodeSignals(body string) string {
	var hints []string
	for _, s := range codeSignals {
		if s.re.MatchString(body) {
			hints = append(hints, s.hint)
		}
	}
	return strings.Join(hints, "; ")
}

// filterAdviceChunks keeps rule/guide corpus chunks + failure-mode corrections
// (dropping process/meta docs), balanced across the avoid/prefer/convention
// buckets so no bucket is starved by another that ranks higher globally — each
// gets up to keep/3+1 of its best, deduped by source (olifant#106, P3).
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
		if seenSource[c.Source] {
			continue
		}
		b := Bucket(c)
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

func adviceNoiseSource(src string) bool {
	for _, p := range adviceNoiseSourcePrefixes {
		if strings.HasPrefix(src, p) {
			return true
		}
	}
	return false
}

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

// Bucket classifies a retrieved chunk as avoid (anti-patterns / failure-mode
// corrections), prefer (proven patterns), or convention/standard to honor.
func Bucket(c prompt.ContextChunk) string {
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
