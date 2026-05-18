package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// retroScopeByProject maps the immediate parent directory of a retro
// file (i.e. the project name) to the training scope.
var retroScopeByProject = map[string]string{
	"core-api":              "backend",
	"akademia-plus-web":     "webapp",
	"elatusdev-web":         "webapp",
	"akademia-plus-central": "mobile",
	"akademia-plus-go":      "mobile",
	"core-api-e2e":          "e2e",
	"infra":                 "infra",
	"platform":              "platform-process",
}

// minRetroSectionBodyChars is the floor for emitting a retro H2
// section — anything shorter is almost certainly a template stub or
// "N/A" and would add noise to the training set.
const minRetroSectionBodyChars = 60

// retroChallengeHints are H2 title substrings (case-insensitive) that
// flip a retro section's role from "domain" to "challenge". These are
// the sections that frame proposed fixes / actions where the model
// should learn to reason about validity.
var retroChallengeHints = []string{
	"improvement actions",
	"failure analysis",
	"anti-patterns discovered",
	"problematic steps",
}

// ExtractRetros walks <kbRoot>/retrospectives/<project>/*.md and emits
// one example per non-trivial H2 section. Sections whose title hints
// at proposed fixes or failures are role:challenge; everything else
// is role:domain. Files whose project is not in retroScopeByProject
// are skipped.
func ExtractRetros(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}
	root := filepath.Join(kbRoot, "retrospectives")
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil, stats, fmt.Errorf("read retrospectives dir: %w", err)
	}

	var out []Example
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		scope, ok := retroScopeByProject[proj.Name()]
		if !ok {
			continue
		}
		pdir := filepath.Join(root, proj.Name())
		files, err := os.ReadDir(pdir)
		if err != nil {
			return nil, stats, fmt.Errorf("read %s: %w", pdir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			path := filepath.Join(pdir, f.Name())
			stats.FilesScanned++
			secs, err := splitH2Sections(path)
			if err != nil {
				return nil, stats, err
			}
			stats.EntriesParsed += len(secs)

			retroStem := strings.TrimSuffix(f.Name(), ".md")
			for _, sec := range secs {
				if len(sec.Body) < minRetroSectionBodyChars {
					continue
				}
				ex := buildRetroExample(proj.Name(), scope, retroStem, sec)
				out = append(out, ex)
				stats.ExamplesEmitted++
				stats.PerScope[ex.Scope]++
			}
		}
	}
	return out, stats, nil
}

func buildRetroExample(project, scope, retroStem string, sec h2Section) Example {
	role := "domain"
	titleLower := strings.ToLower(sec.Title)
	for _, hint := range retroChallengeHints {
		if strings.Contains(titleLower, hint) {
			role = "challenge"
			break
		}
	}

	// Drop leading section numbering like "1. " or "3.1 " so the
	// model isn't trained to depend on retro numbering vocabulary.
	sectionLabel := stripSectionNumberPrefix(sec.Title)

	user := fmt.Sprintf(
		"What did we learn about %q in the %s retrospective (%s)?",
		sectionLabel, project, retroStem,
	)

	meta := map[string]string{
		"project":     project,
		"retro":       retroStem,
		"section":     sec.Title,
	}

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: user},
			{Role: "assistant", Content: sec.Body},
		},
		Tier:     1,
		Scope:    scope,
		Source:   "retrospectives/" + project + "/" + retroStem + ".md#" + slugify(sec.Title),
		Role:     role,
		Family:   "retro-section",
		Metadata: meta,
	}
}

// stripSectionNumberPrefix trims leading "N. " or "N.M " (and small
// variations) so "1. Execution Summary" → "Execution Summary".
func stripSectionNumberPrefix(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return t
	}
	// Scan up to the first non-digit/dot character.
	i := 0
	sawDigit := false
	for i < len(t) {
		c := t[i]
		switch {
		case c >= '0' && c <= '9':
			sawDigit = true
			i++
		case c == '.':
			i++
		default:
			goto end
		}
	}
end:
	if !sawDigit {
		return t
	}
	// Require a separator after the number to avoid eating "9to5"-style titles.
	if i < len(t) && (t[i] == ' ' || t[i] == ')' || t[i] == '-') {
		return strings.TrimSpace(t[i+1:])
	}
	return t
}
