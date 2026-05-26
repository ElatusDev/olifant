package corpus

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Prose tokenises every .md file under cfg.SourceRoot into Sentence
// records with rule-based axes (syntactic_form + modality) populated.
// semantic_role and concern (the LLM-classified axes per workflow
// D-CC4) are left empty for the Day-5 Phase-2 classifier to fill.
//
// Dispatch: like Scan() but always uses extractProse over .md files,
// filtered by isProseCandidate (per-repo: KB needs curated-dir filter).
func Prose(cfg ScanConfig) (ScanStats, error) {
	started := time.Now()
	stats := ScanStats{ByKind: map[string]int{}, ByConcern: map[string]int{}}

	if cfg.RepoRoot == "" {
		return stats, fmt.Errorf("prose: RepoRoot required")
	}
	if cfg.SourceRoot == "" {
		return stats, fmt.Errorf("prose: SourceRoot required")
	}
	if cfg.OutPath == "" && !cfg.DryRun {
		return stats, fmt.Errorf("prose: OutPath required unless DryRun")
	}

	files, err := collectByExts(cfg.SourceRoot, ".md")
	if err != nil {
		return stats, fmt.Errorf("prose: walk %s: %w", cfg.SourceRoot, err)
	}
	var kept []string
	for _, p := range files {
		if isProseCandidate(cfg.Repo, p) {
			kept = append(kept, p)
		}
	}
	if cfg.Verbose && len(files) > 0 {
		fmt.Printf("  %d .md file(s) under %s (%d filtered out)\n",
			len(kept), cfg.SourceRoot, len(files)-len(kept))
	}

	var sentences []Sentence
	for _, path := range kept {
		rel, _ := filepath.Rel(cfg.RepoRoot, path)
		extracted, err := extractProse(path, rel, cfg)
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  WARN %s: %v\n", rel, err)
			}
			continue
		}
		sentences = append(sentences, extracted...)
		stats.FilesScanned++
	}

	// Aggregate per-axis stats (re-using ScanStats's ByKind for syntactic-form
	// and ByConcern for actual concern; modality counts piggyback on ByKind
	// under the "modality:<x>" key to avoid an extra struct field).
	for _, s := range sentences {
		stats.SymbolsEmitted++
		if sf, ok := s.Tags[AxisSyntactic].(string); ok {
			stats.ByKind["syntactic:"+sf]++
		}
		if md, ok := s.Tags[AxisModality].(string); ok {
			stats.ByKind["modality:"+md]++
		}
		if cs, ok := s.Tags[AxisConcern].([]string); ok {
			for _, c := range cs {
				stats.ByConcern[c]++
			}
		}
	}

	stats.Elapsed = time.Since(started)

	if cfg.DryRun {
		return stats, nil
	}

	if err := os.MkdirAll(filepath.Dir(cfg.OutPath), 0o755); err != nil {
		return stats, fmt.Errorf("prose: mkdir out parent: %w", err)
	}
	out, err := os.Create(cfg.OutPath)
	if err != nil {
		return stats, fmt.Errorf("prose: create out: %w", err)
	}
	defer out.Close()
	enc := yaml.NewEncoder(out)
	enc.SetIndent(2)
	if err := enc.Encode(sentences); err != nil {
		return stats, fmt.Errorf("prose: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return stats, fmt.Errorf("prose: close encoder: %w", err)
	}
	return stats, nil
}

// isProseCandidate gates which .md files contribute to prose. For
// knowledge-base, only the curated authoritative sub-dirs count
// (skips retrospectives/, operations/, audit-report/, adr/ to keep
// signal-to-noise high). Other repos: every .md.
func isProseCandidate(repo, path string) bool {
	if repo != "knowledge-base" {
		return true
	}
	norm := "/" + strings.ReplaceAll(path, "\\", "/")
	for _, prefix := range []string{
		"/patterns/", "/anti-patterns/", "/decisions/", "/standards/",
		"/architecture/", "/skills/", "/templates/", "/dictionary/",
		"/runbooks/",
	} {
		if strings.Contains(norm, prefix) {
			return true
		}
	}
	return false
}

// extractProse tokenises one markdown file. Skips code blocks (between
// ```), headers (#-prefixed), HTML tag content, blockquotes (>), and
// table rows (|). Paragraphs split at blank lines; within a paragraph,
// sentences split at [.!?] followed by whitespace + uppercase.
//
// Returns Sentence records with id/text/source/line + rule-based tag
// axes (syntactic_form + modality + scope + concern). The LLM-axes
// (semantic_role + subject_ref) are left for Phase 2.
func extractProse(absPath, relPath string, cfg ScanConfig) ([]Sentence, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scope := scopeFromRepo(cfg.Repo)
	concerns := concernsFromPath(relPath)

	var sentences []Sentence
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	lineNum := 0
	inCode := false

	var paragraph strings.Builder
	paraStart := 0
	flushPara := func() {
		text := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if text == "" {
			return
		}
		for _, s := range splitSentences(text) {
			s = strings.TrimSpace(s)
			if !isMeaningfulSentence(s) {
				continue
			}
			sentences = append(sentences, Sentence{
				ID:     symbolID(relPath, paraStart, s),
				Text:   s,
				Source: relPath,
				Line:   paraStart,
				Tags:   buildProseTags(s, scope, concerns),
			})
		}
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Toggle fenced code block.
		if strings.HasPrefix(trimmed, "```") {
			flushPara()
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		// Skip structural / non-prose lines.
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") ||
			strings.HasPrefix(trimmed, "|") ||
			strings.HasPrefix(trimmed, "---") ||
			strings.HasPrefix(trimmed, "===") ||
			strings.HasPrefix(trimmed, "<!--") {
			flushPara()
			continue
		}
		clean := stripMDInline(line)
		if clean == "" {
			continue
		}
		if paragraph.Len() == 0 {
			paraStart = lineNum
		} else {
			paragraph.WriteString(" ")
		}
		paragraph.WriteString(clean)
	}
	flushPara()
	return sentences, scanner.Err()
}

var (
	reSentenceBoundary = regexp.MustCompile(`([.!?])\s+`)
	reHTMLTag          = regexp.MustCompile(`<[^>]+>`)
	reMDLink           = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reMDBackticks      = regexp.MustCompile("`[^`]+`")
	reMDBoldItalic     = regexp.MustCompile(`\*+([^*]+)\*+`)
	reMDListBullet     = regexp.MustCompile(`^\s*[-*+]\s+`)
	reMDListNumbered   = regexp.MustCompile(`^\s*\d+\.\s+`)
)

// splitSentences naively splits on [.!?] followed by whitespace. False
// splits on e.g./i.e./numeric dots are accepted as v2 noise. Keeps the
// terminating punctuation with the leading sentence.
func splitSentences(text string) []string {
	indices := reSentenceBoundary.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return []string{text}
	}
	out := make([]string, 0, len(indices)+1)
	last := 0
	for _, idx := range indices {
		end := idx[0] + 1 // keep the .!?
		out = append(out, text[last:end])
		last = idx[1]
	}
	if last < len(text) {
		out = append(out, text[last:])
	}
	return out
}

// stripMDInline removes inline markdown decoration so the prose text
// reads more like clean English: HTML tags, links → text, inline code,
// bold/italic, leading list bullets.
func stripMDInline(line string) string {
	s := reMDListBullet.ReplaceAllString(line, "")
	s = reMDListNumbered.ReplaceAllString(s, "")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = reMDLink.ReplaceAllString(s, "$1")
	s = reMDBackticks.ReplaceAllString(s, "")
	s = reMDBoldItalic.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

// isMeaningfulSentence filters fragments that are too short to be
// useful corpus rows (<20 chars), or are URL-only, or are mostly
// punctuation. Length-only check is cheap and catches most noise.
func isMeaningfulSentence(s string) bool {
	if len(s) < 20 {
		return false
	}
	// Need at least one space (no single-token rows).
	if !strings.Contains(s, " ") {
		return false
	}
	// Must contain a letter.
	hasLetter := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			break
		}
	}
	return hasLetter
}

func buildProseTags(text, scope string, concerns []string) map[string]any {
	tags := map[string]any{
		AxisLanguage:  LangMarkdown,
		AxisScope:     scope,
		AxisSyntactic: classifySyntactic(text),
	}
	if mod := classifyModality(text); mod != "" {
		tags[AxisModality] = mod
	}
	if len(concerns) > 0 {
		tags[AxisConcern] = concerns
	}
	return tags
}

// classifySyntactic returns the rule-based syntactic form. Default is
// SyntAffirmation. Checks in priority order: end-punctuation → leading
// conditional / negation / imperative-verb keywords → affirmation.
func classifySyntactic(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return SyntAffirmation
	}
	last := trimmed[len(trimmed)-1]
	switch last {
	case '?':
		return SyntQuestion
	case '!':
		return SyntImperative
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "if ") || strings.Contains(lower, " unless ") || strings.Contains(lower, " otherwise ") {
		return SyntConditional
	}
	if strings.Contains(lower, " not ") || strings.Contains(lower, " never ") ||
		strings.HasPrefix(lower, "no ") || strings.HasPrefix(lower, "do not ") ||
		strings.HasPrefix(lower, "don't ") {
		return SyntNegation
	}
	for _, v := range []string{
		"use ", "run ", "check ", "verify ", "ensure ", "set ", "add ", "remove ",
		"delete ", "create ", "configure ", "install ", "build ", "test ", "deploy ",
	} {
		if strings.HasPrefix(lower, v) {
			return SyntImperative
		}
	}
	return SyntAffirmation
}

// classifyModality returns the strongest matching modality OR empty
// string when none of the keyword cues fire. Checks forbidden first
// (MUST NOT beats MUST), then mandatory, recommended, allowed.
func classifyModality(text string) string {
	upper := strings.ToUpper(text)
	if strings.Contains(upper, "MUST NOT") || strings.Contains(upper, "NEVER ") ||
		strings.Contains(upper, "FORBIDDEN") || strings.Contains(upper, "PROHIBITED") ||
		strings.Contains(upper, "NO EXCEPTIONS") || strings.Contains(upper, "DO NOT ") {
		return ModalForbidden
	}
	if strings.Contains(upper, "MUST ") || strings.Contains(upper, "HARD RULE") ||
		strings.Contains(upper, "REQUIRED") || strings.Contains(upper, "REQUIRES ") ||
		strings.Contains(upper, "MANDATORY") || strings.Contains(upper, "NON-NEGOTIABLE") {
		return ModalMandatory
	}
	if strings.Contains(upper, "SHOULD ") || strings.Contains(upper, "RECOMMEND") ||
		strings.Contains(upper, "PREFER") {
		return ModalRecommended
	}
	if strings.Contains(upper, " MAY ") || strings.Contains(upper, " CAN ") ||
		strings.Contains(upper, "OPTIONAL") || strings.Contains(upper, "ALLOWED") {
		return ModalAllowed
	}
	if strings.HasPrefix(strings.ToLower(text), "if ") || strings.Contains(strings.ToLower(text), " unless ") {
		return ModalConditional
	}
	return ""
}
