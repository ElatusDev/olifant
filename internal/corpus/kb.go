package corpus

import (
	"bufio"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// extractKB dispatches on file extension to a Markdown ID extractor or
// a YAML dictionary-term extractor. Both target the knowledge-base/
// repo's curated sub-dirs (decisions/, anti-patterns/, patterns/,
// dictionary/). Other .md / .yaml under knowledge-base/ are skipped
// at the orchestrator level by isKBNonCurated.
func extractKB(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
	lower := strings.ToLower(relPath)
	switch {
	case strings.HasSuffix(lower, ".md"):
		return extractKBMarkdown(absPath, relPath, cfg)
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return extractKBYAML(absPath, relPath, cfg)
	}
	return nil, nil
}

// extractKBMarkdown extracts H2 / H3 / H4 headers from .md files. If
// the header text starts with a recognised ID prefix (D, AP, PC, FM,
// SB, IV, IMF, WA, AM, AW, AB), the symbol is tagged with kind=id and
// the matching id_family. Otherwise it is tagged kind=class (treating
// the header as a named concept) so it still contributes vocabulary.
//
// Only H2-H4 headers are captured; H1 is the doc title (already
// well-known) and H5+ is sub-structure noise.
func extractKBMarkdown(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scope := scopeFromRepo(cfg.Repo)
	concerns := concernsFromPath(relPath)

	var symbols []Symbol
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		hm := reKBHeader.FindStringSubmatch(line)
		if len(hm) != 3 {
			continue
		}
		text := strings.TrimSpace(hm[2])
		if text == "" {
			continue
		}
		// Try to recognise an ID prefix at the start of the header text.
		if idm := reKBIDPrefix.FindStringSubmatch(text); len(idm) == 3 {
			prefix, num := idm[1], idm[2]
			fam := idFamilyForPrefix(prefix)
			if fam != "" {
				symbols = append(symbols, *mkKBIDSymbol(prefix+num, fam, lineNum, relPath, scope, concerns))
				continue
			}
		}
		// Non-ID headers — capture as named concept (KindClass).
		symbols = append(symbols, *mkKBConceptSymbol(text, lineNum, relPath, scope, concerns))
	}
	return symbols, scanner.Err()
}

// extractKBYAML reads dictionary YAMLs which are top-level lists of
// {term, category, definition, cites, ...} maps. Emits one symbol per
// entry with text = term value. Tag id_family=SB (symbol catalogue)
// and concern from path.
func extractKBYAML(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var entries []dictionaryEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		// Not a list-of-maps YAML — skip silently (this scanner only
		// understands the dictionary shape).
		return nil, nil
	}

	scope := scopeFromRepo(cfg.Repo)
	concerns := concernsFromPath(relPath)

	var symbols []Symbol
	for i, e := range entries {
		if e.Term == "" {
			continue
		}
		// Approximate line number with entry index (yaml.v3's Node API
		// could give exact lines but adds complexity for marginal gain).
		symbols = append(symbols, *mkKBTermSymbol(e.Term, i+1, relPath, scope, concerns))
	}
	return symbols, nil
}

// reKBHeader matches markdown ATX headers H2-H4. Captures level and text.
var reKBHeader = regexp.MustCompile(`^(#{2,4})\s+(.+?)\s*$`)

// reKBIDPrefix matches a leading ID at the start of header text:
//   "D17: ...", "AP3 — ...", "PC1", "AB-04 ..."
// Captures prefix (letters, optional hyphen) + numeric part.
var reKBIDPrefix = regexp.MustCompile(`^([A-Z]{1,4})-?(\d+)\b`)

func idFamilyForPrefix(p string) string {
	switch p {
	case "D":
		return IDFamilyDecision
	case "AP":
		return IDFamilyAntiPattern
	case "PC":
		return IDFamilyPattern
	case "FM":
		return IDFamilyFailureMode
	case "SB":
		return IDFamilySymbol
	case "IV":
		return IDFamilyInputValidation
	case "IMF":
		return IDFamilyImmutableFields
	case "WA":
		return IDFamilyWebappArch
	case "AM", "AMC", "AMP", "AMS", "AMN", "AMH", "AME", "AMTA":
		return IDFamilyMobileAP
	case "AW", "AWC", "AWH", "AWS", "AWR", "AWT", "AWB", "AWTA", "AWA":
		return IDFamilyWebappAP
	case "AB", "ABB", "ABO", "ABC", "ABD", "ABE", "ABS", "ABT":
		return IDFamilyBackendAP
	}
	return ""
}

func mkKBIDSymbol(text, idFamily string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangMarkdown,
		AxisKind:     KindID,
		AxisScope:    scope,
		AxisIDFamily: idFamily,
	}
	if len(concerns) > 0 {
		tags[AxisConcern] = concerns
	}
	return &Symbol{
		ID:     symbolID(source, lineNum, text),
		Text:   text,
		Source: source,
		Line:   lineNum,
		Tags:   tags,
	}
}

func mkKBConceptSymbol(text string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangMarkdown,
		AxisKind:     KindClass, // re-using KindClass as "named concept" for KB headers
		AxisScope:    scope,
	}
	if len(concerns) > 0 {
		tags[AxisConcern] = concerns
	}
	return &Symbol{
		ID:     symbolID(source, lineNum, text),
		Text:   text,
		Source: source,
		Line:   lineNum,
		Tags:   tags,
	}
}

func mkKBTermSymbol(text string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangYAML,
		AxisKind:     KindTerm,
		AxisScope:    scope,
	}
	if len(concerns) > 0 {
		tags[AxisConcern] = concerns
	}
	return &Symbol{
		ID:     symbolID(source, lineNum, text),
		Text:   text,
		Source: source,
		Line:   lineNum,
		Tags:   tags,
	}
}

type dictionaryEntry struct {
	Term       string   `yaml:"term"`
	Category   string   `yaml:"category"`
	Definition string   `yaml:"definition"`
	Cites      []string `yaml:"cites"`
}

// isKBNonCurated returns true for KB files outside the 4 curated
// sub-dirs (decisions/, anti-patterns/, patterns/, dictionary/). Used
// by the orchestrator to filter the walk. Prepends '/' before substring
// checks so bare-prefix paths ("decisions/log.md") still match.
func isKBNonCurated(path string) bool {
	norm := "/" + strings.ReplaceAll(path, "\\", "/")
	for _, prefix := range []string{"/decisions/", "/anti-patterns/", "/patterns/", "/dictionary/"} {
		if strings.Contains(norm, prefix) {
			return false
		}
	}
	return true
}
