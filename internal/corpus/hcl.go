package corpus

import (
	"bufio"
	"os"
	"regexp"
)

// extractHCL scans a Terraform .tf file and emits Symbols for the
// top-level block kinds that anchor an HCL module's vocabulary:
// resource, data, module, variable, output, provider. Resource and
// data blocks carry the AWS-side type (e.g. aws_s3_bucket) AND the
// local name; we emit the text as "type.name" so the corpus learns
// both halves together.
//
// Regex-based, line-anchored. Matches only top-level block openings
// (column-0 to whitespace then the keyword). Nested blocks and
// expressions are out of scope for v2.
func extractHCL(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
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
		if sym := matchHCLResource(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchHCLData(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchHCLModule(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchHCLVariable(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchHCLOutput(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
			continue
		}
		if sym := matchHCLProvider(line, lineNum, relPath, scope, concerns); sym != nil {
			symbols = append(symbols, *sym)
		}
	}
	return symbols, scanner.Err()
}

var (
	reHCLResource = regexp.MustCompile(`^resource\s+"([^"]+)"\s+"([^"]+)"\s*\{`)
	reHCLData     = regexp.MustCompile(`^data\s+"([^"]+)"\s+"([^"]+)"\s*\{`)
	reHCLModule   = regexp.MustCompile(`^module\s+"([^"]+)"\s*\{`)
	reHCLVariable = regexp.MustCompile(`^variable\s+"([^"]+)"\s*\{`)
	reHCLOutput   = regexp.MustCompile(`^output\s+"([^"]+)"\s*\{`)
	reHCLProvider = regexp.MustCompile(`^provider\s+"([^"]+)"\s*\{`)
)

func matchHCLResource(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLResource.FindStringSubmatch(line); len(m) == 3 {
		return mkHCLSymbol(KindResource, m[1]+"."+m[2], lineNum, source, scope, concerns)
	}
	return nil
}

func matchHCLData(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLData.FindStringSubmatch(line); len(m) == 3 {
		// Data sources reuse KindResource — semantically similar (named
		// reference to a typed AWS object); language axis disambiguates.
		return mkHCLSymbol(KindResource, "data."+m[1]+"."+m[2], lineNum, source, scope, concerns)
	}
	return nil
}

func matchHCLModule(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLModule.FindStringSubmatch(line); len(m) == 2 {
		return mkHCLSymbol(KindModule, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchHCLVariable(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLVariable.FindStringSubmatch(line); len(m) == 2 {
		return mkHCLSymbol(KindVariable, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchHCLOutput(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLOutput.FindStringSubmatch(line); len(m) == 2 {
		return mkHCLSymbol(KindOutput, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func matchHCLProvider(line string, lineNum int, source, scope string, concerns []string) *Symbol {
	if m := reHCLProvider.FindStringSubmatch(line); len(m) == 2 {
		return mkHCLSymbol(KindConfigKey, m[1], lineNum, source, scope, concerns)
	}
	return nil
}

func mkHCLSymbol(kind, text string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangHCL,
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
