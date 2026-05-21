package corpus

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// extractTypeScript scans a TS/TSX file and emits Symbols for the
// React / RTK-Query shapes that matter for the v2 curriculum:
// components, hooks, interfaces, type aliases, enums,
// SCREAMING_SNAKE_CASE constants, and RTK Query endpoint declarations.
//
// Plain camelCase top-level functions/consts are intentionally skipped
// (utility helpers — high noise / low signal for the corpus). Test
// files are filtered at the orchestrator level (isTestFile), not here.
//
// Regex-based, mirroring extractJava — no tree-sitter dep. Line-anchored
// patterns; one line yields at most one symbol per check.
func extractTypeScript(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
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
		if sym := matchTSInterface(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchTSTypeAlias(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchTSEnum(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchTSFuncOrComponent(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchTSEndpoint(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchTSConstant(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
		}
	}
	return symbols, scanner.Err()
}

// regexes — line-anchored. Conservative on purpose; v2 favours
// signal-to-noise over completeness. Complex TS surface (mapped types,
// conditional types, multi-line generic param lists) is intentionally
// not modelled here.
var (
	reTSInterface = regexp.MustCompile(`^\s*(?:export\s+(?:default\s+)?)?interface\s+(\w+)`)
	reTSTypeAlias = regexp.MustCompile(`^\s*(?:export\s+(?:default\s+)?)?type\s+(\w+)\s*[<=]`)
	reTSEnum      = regexp.MustCompile(`^\s*(?:export\s+(?:default\s+)?)?(?:const\s+)?enum\s+(\w+)`)
	reTSFuncDecl  = regexp.MustCompile(`^\s*(?:export\s+(?:default\s+)?)?(?:async\s+)?function\s+(\w+)\s*[<(]`)
	reTSConstFunc = regexp.MustCompile(`^\s*(?:export\s+(?:default\s+)?)?const\s+(\w+)\s*(?::[^=]+)?\s*=\s*(?:async\s+)?\(`)
	reTSConstant  = regexp.MustCompile(`^\s*(?:export\s+)?const\s+([A-Z][A-Z0-9_]+)\s*(?::[^=]+)?\s*=`)
	reTSEndpoint  = regexp.MustCompile(`^\s+(\w+)\s*:\s*build(?:er)?\.(query|mutation|infiniteQuery)\b`)
)

func matchTSInterface(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reTSInterface.FindStringSubmatch(line); len(m) == 2 {
		return mkTSSymbol(KindInterface, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchTSTypeAlias(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reTSTypeAlias.FindStringSubmatch(line); len(m) == 2 {
		return mkTSSymbol(KindType, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchTSEnum(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reTSEnum.FindStringSubmatch(line); len(m) == 2 {
		return mkTSSymbol(KindEnum, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

// matchTSFuncOrComponent classifies a matched callable by naming convention.
// useXxx → hook, Xxx (PascalCase) → component, otherwise → skip (helper).
func matchTSFuncOrComponent(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	var name string
	if m := reTSFuncDecl.FindStringSubmatch(line); len(m) == 2 {
		name = m[1]
	} else if m := reTSConstFunc.FindStringSubmatch(line); len(m) == 2 {
		name = m[1]
	} else {
		return nil
	}
	kind := classifyTSCallable(name)
	if kind == "" {
		return nil
	}
	return mkTSSymbol(kind, name, lineNum, source, scope, concerns)
}

func matchTSEndpoint(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reTSEndpoint.FindStringSubmatch(line); len(m) >= 2 {
		return mkTSSymbol(KindEndpoint, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchTSConstant(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reTSConstant.FindStringSubmatch(line); len(m) == 2 {
		return mkTSSymbol(KindConstant, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func classifyTSCallable(name string) string {
	if len(name) < 2 {
		return ""
	}
	// useXxx (lowercase 'use' followed by uppercase letter) = hook.
	if strings.HasPrefix(name, "use") && len(name) >= 4 && name[3] >= 'A' && name[3] <= 'Z' {
		return KindHook
	}
	// PascalCase = component.
	if name[0] >= 'A' && name[0] <= 'Z' {
		return KindComponent
	}
	return ""
}

func mkTSSymbol(kind, text string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangTypeScript,
		AxisKind:     kind,
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

// isTestFile returns true for any TS file the corpus should exclude:
// *.test.ts, *.test.tsx, *.spec.ts, *.spec.tsx, anything under
// __tests__/, e2e/, or tests/. Cross-platform-safe: matches both
// '/' and '\' separators. A leading '/' is prepended before substring
// checks so bare-prefix paths like "tests/integration/x.ts" still match.
func isTestFile(path string) bool {
	norm := "/" + strings.ReplaceAll(path, "\\", "/")
	if strings.Contains(norm, "/__tests__/") ||
		strings.Contains(norm, "/e2e/") ||
		strings.Contains(norm, "/tests/") {
		return true
	}
	lower := strings.ToLower(norm)
	for _, suffix := range []string{".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}
