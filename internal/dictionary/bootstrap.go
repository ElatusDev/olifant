package dictionary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BootstrapConfig drives heuristic dictionary seeding from a built corpus.
//
// This is the artifact-ID-only first layer. It walks corpus NDJSON files and
// emits one domain.yaml entry per chunk that carries an `artifact_id`. LLM-
// based bootstrap (proposing nouns/subjects/verbs from prose) is a future
// layer; this layer is deterministic and idempotent.
type BootstrapConfig struct {
	CorpusDir     string // <kb-root>/corpus/v1
	DictionaryDir string // <kb-root>/dictionary
	Verbose       bool
	DryRun        bool // when true, print what would be written, change nothing
}

// Bootstrap runs the seeding. Returns summary stats.
type BootstrapStats struct {
	ChunksRead       int
	WithArtifactID   int
	UniqueArtifactID int
	EntriesAdded     int
	EntriesSkipped   int // term already present
	PerScopeAdded    map[string]int
	PerCategoryAdded map[string]int
}

// Bootstrap walks corpus NDJSON and merges artifact-derived entries into
// dictionary/<scope>/domain.yaml. Existing entries (matched by `term`) are
// kept as-is — never overwritten. Re-runs are no-ops once the file is full.
func Bootstrap(cfg BootstrapConfig) (BootstrapStats, error) {
	stats := BootstrapStats{
		PerScopeAdded:    map[string]int{},
		PerCategoryAdded: map[string]int{},
	}

	type proposed struct {
		entry  Entry
		scope  string
	}
	seen := map[string]proposed{}

	entries, err := os.ReadDir(cfg.CorpusDir)
	if err != nil {
		return stats, fmt.Errorf("read corpus dir %s: %w", cfg.CorpusDir, err)
	}
	for _, f := range entries {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".ndjson") {
			continue
		}
		path := filepath.Join(cfg.CorpusDir, f.Name())
		fh, err := os.Open(path)
		if err != nil {
			return stats, err
		}
		scan := bufio.NewScanner(fh)
		scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scan.Scan() {
			stats.ChunksRead++
			var c struct {
				ArtifactID   string `json:"artifact_id"`
				Title        string `json:"title"`
				Body         string `json:"body"`
				Source       string `json:"source"`
				SourceAnchor string `json:"source_anchor"`
				Scope        string `json:"scope"`
				DocType      string `json:"doc_type"`
			}
			if err := json.Unmarshal(scan.Bytes(), &c); err != nil {
				continue
			}
			if c.ArtifactID == "" {
				continue
			}
			stats.WithArtifactID++

			// First-write-wins per term (corpus is sorted; first chunk
			// for a term is the canonical-looking one).
			if _, present := seen[c.ArtifactID]; present {
				continue
			}
			category := categorizeArtifactID(c.ArtifactID, c.DocType)
			if category == "" {
				continue
			}
			definition := bestDefinition(c.Title, c.Body)
			cite := c.SourceAnchor
			if cite == "" {
				cite = c.Source
			}
			seen[c.ArtifactID] = proposed{
				entry: Entry{
					Term:         c.ArtifactID,
					Category:     category,
					Definition:   definition,
					Cites:        []string{cite},
					Introduced:   time.Now().UTC().Format("2006-01-02"),
					IntroducedBy: "corpus-bootstrap",
				},
				scope: scopeForCategory(category, c.Scope),
			}
		}
		fh.Close()
	}
	stats.UniqueArtifactID = len(seen)

	// Bucket proposals by target scope
	byScope := map[string][]Entry{}
	for _, p := range seen {
		byScope[p.scope] = append(byScope[p.scope], p.entry)
	}

	// Merge into each scope's domain.yaml
	for scope, props := range byScope {
		sort.Slice(props, func(i, j int) bool { return props[i].Term < props[j].Term })

		dir := filepath.Join(cfg.DictionaryDir, scope)
		path := filepath.Join(dir, "domain.yaml")
		existing, err := readDomain(path)
		if err != nil {
			return stats, fmt.Errorf("read %s: %w", path, err)
		}
		existingTerms := map[string]struct{}{}
		for _, e := range existing {
			existingTerms[e.Term] = struct{}{}
		}

		var added []Entry
		for _, p := range props {
			if _, ok := existingTerms[p.Term]; ok {
				stats.EntriesSkipped++
				continue
			}
			added = append(added, p)
			stats.EntriesAdded++
			stats.PerScopeAdded[scope]++
			stats.PerCategoryAdded[p.Category]++
		}
		if len(added) == 0 {
			continue
		}
		merged := append(existing, added...)
		sort.Slice(merged, func(i, j int) bool { return merged[i].Term < merged[j].Term })

		if cfg.DryRun {
			fmt.Printf("[dry-run] would write %d entries to %s (existing: %d, new: %d)\n",
				len(merged), path, len(existing), len(added))
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return stats, err
		}
		if err := writeDomain(path, merged); err != nil {
			return stats, fmt.Errorf("write %s: %w", path, err)
		}
		if cfg.Verbose {
			fmt.Printf("  %-22s  +%d entries (total %d) → %s\n",
				scope, len(added), len(merged), path)
		}
	}
	return stats, nil
}

// readDomain returns the existing entries of a scope's domain.yaml, or empty
// slice if the file doesn't exist yet.
func readDomain(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	if err := yaml.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// writeDomain emits entries with a frontmatter-style header for human readers.
func writeDomain(path string, entries []Entry) error {
	header := `# Olifant CNL — domain dictionary (mutable; grows via challenge step).
# Spec: ../../dsl/cnl-v1.md
# Seeded by olifant corpus-bootstrap; future entries appended through the
# challenge step.

`
	body, err := yaml.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(header+string(body)), 0o644)
}

// categorizeArtifactID maps an artifact_id to a CNL domain.yaml category.
// Returns "" for unrecognized patterns (entry is silently skipped).
func categorizeArtifactID(id, docType string) string {
	switch {
	case reDecision.MatchString(id):
		return "domain.decision"
	case reAntiPatternTop.MatchString(id):
		return "domain.anti_pattern"
	case reAPBackend.MatchString(id):
		return "domain.anti_pattern.backend"
	case reAPWebapp.MatchString(id):
		return "domain.anti_pattern.webapp"
	case reAPMobile.MatchString(id):
		return "domain.anti_pattern.mobile"
	case reStandardSec.MatchString(id):
		return "domain.standard_rule.security"
	case reArchWebapp.MatchString(id):
		return "domain.standard_rule.architecture.webapp"
	case reTestBackend.MatchString(id):
		return "domain.standard_rule.testing.backend"
	case reTestWebapp.MatchString(id):
		return "domain.standard_rule.testing.webapp"
	case reTestMobile.MatchString(id):
		return "domain.standard_rule.testing.mobile"
	case reObs.MatchString(id):
		return "domain.standard_rule.observability"
	case reQuality.MatchString(id):
		return "domain.standard_rule.quality"
	case reSchemaSrc.MatchString(id):
		return "domain.standard_rule.schema"
	case reRetroDiscipline.MatchString(id):
		return "domain.retrospective_rule"
	}
	return ""
}

// scopeForCategory returns the dictionary scope a category lives in.
// Stack-specific anti-patterns and rules go to that stack's dictionary;
// everything else lands in universal.
func scopeForCategory(category, chunkScope string) string {
	switch {
	case strings.HasSuffix(category, ".backend"):
		return ScopeBackend
	case strings.HasSuffix(category, ".webapp"):
		return ScopeWebapp
	case strings.HasSuffix(category, ".mobile"):
		return ScopeMobile
	}
	return ScopeUniversal
}

// bestDefinition picks the most useful one-liner from a chunk's title and body.
// Falls back to a sanitized first sentence if no title is set.
func bestDefinition(title, body string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	// strip our breadcrumb header line if present
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "[") {
		if nl := strings.IndexByte(body, '\n'); nl > 0 {
			body = strings.TrimSpace(body[nl+1:])
		}
	}
	// first sentence (up to . ! ? or newline)
	end := strings.IndexAny(body, ".!?\n")
	if end > 0 {
		body = body[:end]
	}
	body = strings.TrimSpace(body)
	if len(body) > 240 {
		body = body[:240] + "…"
	}
	return body
}

// Compiled regexes — declared once.
var (
	reDecision        = regexp.MustCompile(`^D\d+$`)
	reAntiPatternTop  = regexp.MustCompile(`^AP\d+$`)
	reAPBackend       = regexp.MustCompile(`^(ABB|ABO|ABC|ABD|ABE|ABS|ABT)-\d+$`)
	reAPWebapp        = regexp.MustCompile(`^(AWC|AWH|AWS|AWR|AWT|AWB|AWTA|AWA)-\d+$`)
	reAPMobile        = regexp.MustCompile(`^(AMC|AMP|AMS|AMN|AMH|AME|AMTA)-\d+$`)
	reStandardSec     = regexp.MustCompile(`^(SB|SI|SW|SM|SX)-\d+$`)
	reArchWebapp      = regexp.MustCompile(`^WA-[A-Z]+(?:-\d+)?$`)
	reTestBackend     = regexp.MustCompile(`^(TBX|TBU|TBC|TBE|TAP)-\d+$`)
	reTestWebapp      = regexp.MustCompile(`^(TWU|TWC|TWE|TAW)-\d+$`)
	reTestMobile      = regexp.MustCompile(`^(TMU|TMC|TME|TAM)-\d+$`)
	reObs             = regexp.MustCompile(`^(OL|OT|OM|OH|OE|OW|OA|OI|AO)-\d+$`)
	reQuality         = regexp.MustCompile(`^[UBWMTEI]-\d+$`)
	reSchemaSrc       = regexp.MustCompile(`^SS-\d+$`)
	reRetroDiscipline = regexp.MustCompile(`^(RL|RK|RM|RS)\d+$`)
)
