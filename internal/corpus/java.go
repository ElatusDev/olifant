package corpus

import (
	"bufio"
	"os"
	"regexp"
)

// extractJava is the Day 1 minimal Java extractor: regex-based scan for
// package + class/interface/enum/annotation declarations. Tree-sitter-java
// integration lands in a follow-up turn — regex covers ~95% of Java's
// surface for OUR purpose (identifier extraction, not full semantic
// analysis). Day 2's scale test will surface any cases where regex misses
// and motivate the tree-sitter upgrade.
//
// For each source file the extractor emits:
//   - 1× kind=package symbol (the package declaration)
//   - N× kind=class symbols (one per top-level + nested class)
//   - M× kind=interface symbols
//   - K× kind=annotation symbols (declarations like `@interface X { ... }`)
//   - E× kind=enum symbols
//
// Method + field + annotation-usage extraction lands next turn.
func extractJava(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scope := scopeFromRepo(cfg.Repo)
	concerns := concernsFromPath(relPath)

	var symbols []Symbol
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<24) // tolerate long lines
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if sym := matchJavaPackage(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchJavaTypeDecl(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
		}
	}
	return symbols, scanner.Err()
}

// java regexes — line-anchored to avoid catching usages inside strings/comments.
var (
	rePackage   = regexp.MustCompile(`^\s*package\s+([\w.]+)\s*;`)
	reClass     = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+|final\s+|abstract\s+|sealed\s+|non-sealed\s+)*class\s+(\w+)`)
	reInterface = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+|sealed\s+|non-sealed\s+)*interface\s+(\w+)`)
	reAnno      = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+)?@interface\s+(\w+)`)
	reEnum      = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+|final\s+)*enum\s+(\w+)`)
)

func matchJavaPackage(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := rePackage.FindStringSubmatch(line); len(m) == 2 {
		return &Symbol{
			ID:     symbolID(source, lineNum, m[1]),
			Text:   m[1],
			Source: source,
			Line:   lineNum,
			Tags:   buildJavaTags(KindPackage, scope, concerns),
		}
	}
	return nil
}

func matchJavaTypeDecl(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	// Check order matters: @interface before interface, otherwise reInterface
	// would falsely match `@interface X` if Java permitted that (it doesn't,
	// but defensive ordering keeps regexes robust to future expansion).
	if m := reAnno.FindStringSubmatch(line); len(m) == 2 {
		return &Symbol{
			ID:     symbolID(source, lineNum, m[1]),
			Text:   m[1],
			Source: source,
			Line:   lineNum,
			Tags:   buildJavaTags(KindAnnotation, scope, concerns),
		}
	}
	if m := reClass.FindStringSubmatch(line); len(m) == 2 {
		return &Symbol{
			ID:     symbolID(source, lineNum, m[1]),
			Text:   m[1],
			Source: source,
			Line:   lineNum,
			Tags:   buildJavaTags(KindClass, scope, concerns),
		}
	}
	if m := reInterface.FindStringSubmatch(line); len(m) == 2 {
		return &Symbol{
			ID:     symbolID(source, lineNum, m[1]),
			Text:   m[1],
			Source: source,
			Line:   lineNum,
			Tags:   buildJavaTags(KindInterface, scope, concerns),
		}
	}
	if m := reEnum.FindStringSubmatch(line); len(m) == 2 {
		return &Symbol{
			ID:     symbolID(source, lineNum, m[1]),
			Text:   m[1],
			Source: source,
			Line:   lineNum,
			Tags:   buildJavaTags(KindEnum, scope, concerns),
		}
	}
	return nil
}

// buildJavaTags assembles the multi-axis tag map for a Java symbol.
func buildJavaTags(kind, scope string, concerns []string) map[string]any {
	t := map[string]any{
		AxisLanguage: LangJava,
		AxisKind:     kind,
		AxisScope:    scope,
	}
	if len(concerns) > 0 {
		t[AxisConcern] = concerns
	}
	return t
}

// scopeFromRepo maps olifant's repo names to KB scope conventions.
func scopeFromRepo(repo string) string {
	switch repo {
	case "core-api":
		return ScopeBackend
	case "akademia-plus-web", "elatusdev-web":
		return ScopeWebapp
	case "akademia-plus-central", "akademia-plus-go":
		return ScopeMobile
	case "core-api-e2e":
		return ScopeE2E
	case "infra":
		return ScopeInfra
	case "knowledge-base":
		return ScopePlatformProcess
	default:
		return ScopeUniversal
	}
}

// concernsFromPath infers concern tags from path keywords. Multi-valued
// because a class can be both security and persistence (e.g. tenant
// guard filters touch both).
func concernsFromPath(relPath string) []string {
	var out []string
	add := func(c string) {
		for _, e := range out {
			if e == c {
				return
			}
		}
		out = append(out, c)
	}
	lower := relPath
	for _, m := range []struct {
		needle  string
		concern string
	}{
		{"security", ConcernSecurity},
		{"auth", ConcernSecurity},
		{"jwt", ConcernSecurity},
		{"passkey", ConcernSecurity},
		{"multi-tenant", ConcernTenancy},
		{"multitenant", ConcernTenancy},
		{"tenant", ConcernTenancy},
		{"repository", ConcernPersistence},
		{"datamodel", ConcernPersistence},
		{"migration", ConcernPersistence},
		{"sqldelete", ConcernPersistence},
		{"observability", ConcernObservability},
		{"logging", ConcernObservability},
		{"metric", ConcernObservability},
		{"controller", ConcernAPIContract},
		{"dto", ConcernAPIContract},
		{"endpoint", ConcernAPIContract},
		{"/test/", ConcernTesting},
		{"src/test", ConcernTesting},
		// TS / webapp / mobile path heuristics. Added 2026-05-21 for
		// Day 3 (TS extractor). Anchored substrings ('/components/' etc.)
		// avoid colliding with Java class names that contain the words.
		{"/components/", ConcernUI},
		{"/pages/", ConcernUI},
		{"/screens/", ConcernUI},
		{"/theme/", ConcernUI},
		{"/i18n/", ConcernUI},
		{"/locales/", ConcernUI},
	} {
		if containsCI(lower, m.needle) {
			add(m.concern)
		}
	}
	return out
}

// containsCI is a case-insensitive substring check that avoids
// allocating a lowercased copy of the whole path on every match.
func containsCI(s, needle string) bool {
	// Both args are short — Go's stdlib does case-folded compare via
	// strings.EqualFold but only on slices; for substring search we
	// roll our own without an allocation.
	if len(needle) == 0 || len(s) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a, b := s[i+j], needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
