package dataset

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// patternScopeByBasename maps the patterns/<file>.md basename to the
// scope to emit. testing is cross-cutting → universal;
// prompt-engineering is meta → platform-process.
var patternScopeByBasename = map[string]string{
	"backend":            "backend",
	"frontend":           "webapp",
	"mobile":             "mobile",
	"testing":            "universal",
	"infrastructure":     "infra",
	"prompt-engineering": "platform-process",
}

// ExtractPatterns walks <kbRoot>/patterns/*.md and emits one
// role:domain example per H2 section. Each H2 is taken as a
// pattern name; the body is everything until the next H2 (or EOF).
// Files whose basename isn't in patternScopeByBasename are skipped
// to keep scope assignment deterministic.
func ExtractPatterns(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}
	dir := filepath.Join(kbRoot, "patterns")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, stats, fmt.Errorf("read patterns dir: %w", err)
	}

	var out []Example
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		base := strings.TrimSuffix(ent.Name(), ".md")
		scope, ok := patternScopeByBasename[base]
		if !ok {
			continue
		}
		stats.FilesScanned++

		path := filepath.Join(dir, ent.Name())
		// Pattern files use either H2 (backend.md) or H3 (frontend/
		// mobile/testing/infrastructure/prompt-engineering) as the
		// per-pattern boundary. Detect which level the file uses,
		// then split on that level.
		level, err := detectPatternHeadingLevel(path)
		if err != nil {
			return nil, stats, err
		}
		secs, err := splitHeadingSections(path, level)
		if err != nil {
			return nil, stats, err
		}
		stats.EntriesParsed += len(secs)

		for _, sec := range secs {
			ex := buildPatternExample(base, scope, sec)
			out = append(out, ex)
			stats.ExamplesEmitted++
			stats.PerScope[ex.Scope]++
		}
	}
	return out, stats, nil
}

type h2Section struct {
	Title string // header text without leading "## " (or "### " for H3)
	Body  string // everything between this header and the next (or EOF), trimmed
}

// detectPatternHeadingLevel returns 2 if the file contains any H2
// outside code fences, else 3. Used to pick the split level for
// patterns/*.md where convention varies per file.
func detectPatternHeadingLevel(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	inFence := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			return 2, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", path, err)
	}
	return 3, nil
}

// splitHeadingSections is the level-parameterized form of
// splitH2Sections. level=2 splits on `## `, level=3 splits on `### `.
func splitHeadingSections(path string, level int) ([]h2Section, error) {
	prefix := strings.Repeat("#", level) + " "
	return splitOnPrefix(path, prefix)
}

// splitH2Sections is the historical entry point kept for retros + the
// existing tests. Delegates to splitHeadingSections for H2.
func splitH2Sections(path string) ([]h2Section, error) {
	return splitHeadingSections(path, 2)
}

func splitOnPrefix(path, prefix string) ([]h2Section, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var (
		secs    []h2Section
		cur     *h2Section
		inFence bool
		body    strings.Builder
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // patterns/*.md can have long code blocks
	for sc.Scan() {
		line := sc.Text()
		// Track fenced code blocks.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, prefix) {
			// Close previous section.
			if cur != nil {
				cur.Body = strings.TrimSpace(body.String())
				secs = append(secs, *cur)
				body.Reset()
			}
			title := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			cur = &h2Section{Title: title}
			continue
		}
		if cur != nil {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if cur != nil {
		cur.Body = strings.TrimSpace(body.String())
		secs = append(secs, *cur)
	}
	return secs, nil
}

func buildPatternExample(catalog, scope string, sec h2Section) Example {
	user := fmt.Sprintf("How do we handle %q in the %s stack on ElatusDev/AkademiaPlus?", sec.Title, catalog)

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: user},
			{Role: "assistant", Content: sec.Body},
		},
		Tier:   1,
		Scope:  scope,
		Source: "patterns/" + catalog + ".md#" + slugify(sec.Title),
		Role:   "domain",
		Family: "pattern-section",
		Metadata: map[string]string{
			"catalog":      catalog,
			"pattern_name": sec.Title,
		},
	}
}

// slugify converts a pattern title into a stable URL-fragment-ish id
// for the Source field. ASCII-lower, spaces → hyphens, strip
// punctuation. Good enough for citation lookup; not RFC-3986.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '-' || r == '_':
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
