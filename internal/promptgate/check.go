package promptgate

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ElatusDev/olifant/internal/corpus"
)

// CitedItem is one citation found in a document, with its first location.
type CitedItem struct {
	Cite    string  `yaml:"cite"`
	Line    int     `yaml:"line"`
	Verdict Verdict `yaml:"verdict"`
	Source  string  `yaml:"source,omitempty"`
}

// DocReport is the cite-gate verdict for one document. Pass means zero
// unresolved cites; stale cites do not fail the gate (D-OP7) — they signal
// the corpus index is behind the sources, an operator concern.
type DocReport struct {
	Doc        string      `yaml:"doc"`
	Pass       bool        `yaml:"pass"`
	Resolved   int         `yaml:"resolved"`
	Stale      int         `yaml:"stale"`
	Unresolved int         `yaml:"unresolved"`
	Items      []CitedItem `yaml:"items,omitempty"`
}

// pathCitePrefixes gates which slash-containing tokens are judged as path
// cites. Tokens outside these prefixes (e.g. olifant-internal paths like
// internal/foo/bar.go) are ignored, not failed — the gate only judges what
// the platform-rooted validator can actually resolve, keeping precision high.
// Mirrors the intent of the validator's repoPrefixes without reaching into it.
var pathCitePrefixes = []string{
	"knowledge-base/", "core-api/", "akademia-plus-web/", "elatusdev-web/",
	"akademia-plus-central/", "akademia-plus-go/", "core-api-e2e/", "infra/",
	"decisions/", "anti-patterns/", "standards/", "patterns/", "workflows/",
	"prompts/", "retrospectives/", "templates/", "skills/", "operations/",
	"architecture/", "audit-report/", "concepts/", "constraints/",
	"glossary/", "dsl/",
}

// pathTokenRE matches platform-style relative path tokens with an extension,
// tolerating #anchor / :line suffixes which are stripped before resolution.
var pathTokenRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_./+-]*/[A-Za-z0-9_./+-]+\.[A-Za-z0-9_]+(?:#[A-Za-z0-9_-]+|:[0-9]+(?:-[0-9]+)?)?`)

// CheckDoc reads a markdown document and resolves every citation in it:
// bare artifact IDs (per the corpus cite patterns) and prefix-gated path
// cites. Deterministic and offline — no Chroma, no Ollama, no LLM.
func (r *Resolver) CheckDoc(path string) (*DocReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read doc: %w", err)
	}

	report := &DocReport{Doc: path}
	seen := map[string]bool{}

	record := func(cite string, line int) {
		if seen[cite] {
			return
		}
		seen[cite] = true
		res := r.Resolve(cite)
		report.Items = append(report.Items, CitedItem{
			Cite: cite, Line: line, Verdict: res.Verdict, Source: res.Source,
		})
		switch res.Verdict {
		case VerdictResolved:
			report.Resolved++
		case VerdictStale:
			report.Stale++
		case VerdictUnresolved:
			report.Unresolved++
		}
	}

	for i, lineText := range strings.Split(string(raw), "\n") {
		lineNo := i + 1
		for _, id := range corpus.ExtractCites(lineText) {
			record(id, lineNo)
		}
		for _, tok := range pathTokenRE.FindAllString(lineText, -1) {
			cite := strings.TrimRight(tok, ".,;)")
			if !hasPathCitePrefix(cite) {
				continue
			}
			record(stripLineSuffix(cite), lineNo)
		}
	}

	report.Pass = report.Unresolved == 0
	return report, nil
}

func hasPathCitePrefix(tok string) bool {
	for _, p := range pathCitePrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

// stripLineSuffix drops a trailing :N / :N-M line reference (the resolver
// handles #anchor itself, mirroring the validator).
func stripLineSuffix(tok string) string {
	if idx := strings.LastIndexByte(tok, ':'); idx > 0 {
		if isLineRef(tok[idx+1:]) {
			return tok[:idx]
		}
	}
	return tok
}

func isLineRef(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
