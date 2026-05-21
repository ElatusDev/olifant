package corpus

import (
	"encoding/json"
	"os"
	"strings"
)

// extractPostman parses a Postman v2.x collection JSON and emits one
// Symbol per request (KindEndpoint) plus one per collection-level name
// (KindResource). Folder names are also captured as KindResource so
// downstream generation can re-create the request hierarchy. Variable
// declarations land as KindConfigKey.
//
// Unlike the regex extractors, this one is structural: it parses the
// full JSON tree and walks it. Items in Postman can nest indefinitely
// (folders within folders), so walkPostmanItems recurses.
//
// If info.name is missing the file is skipped silently (likely not a
// Postman collection — e.g. an env file or an unrelated JSON).
func extractPostman(absPath, relPath string, cfg ScanConfig) ([]Symbol, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var coll postmanCollection
	if err := json.Unmarshal(data, &coll); err != nil {
		// Malformed JSON — return empty rather than fail the whole scan.
		return nil, nil
	}
	if coll.Info.Name == "" {
		return nil, nil
	}

	scope := scopeFromRepo(cfg.Repo)
	concerns := concernsFromPath(relPath)

	var symbols []Symbol
	// Collection name → KindResource (lineNum 1: top of file).
	symbols = append(symbols, *mkPostmanSymbol(KindResource, coll.Info.Name, 1, relPath, scope, concerns))
	// Walk items recursively.
	walkPostmanItems(coll.Item, relPath, scope, concerns, &symbols)
	// Variables.
	for _, v := range coll.Variable {
		if v.Key == "" {
			continue
		}
		symbols = append(symbols, *mkPostmanSymbol(KindConfigKey, v.Key, 1, relPath, scope, concerns))
	}
	return symbols, nil
}

func walkPostmanItems(items []postmanItem, source, scope string, concerns []string, out *[]Symbol) {
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		if len(it.Item) > 0 {
			// Folder — has nested items.
			*out = append(*out, *mkPostmanSymbol(KindResource, it.Name, 1, source, scope, concerns))
			walkPostmanItems(it.Item, source, scope, concerns, out)
			continue
		}
		// Leaf request — no nested items.
		if it.Request != nil {
			*out = append(*out, *mkPostmanSymbol(KindEndpoint, it.Name, 1, source, scope, concerns))
		}
	}
}

type postmanCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item     []postmanItem     `json:"item"`
	Variable []postmanVariable `json:"variable"`
}

type postmanItem struct {
	Name    string          `json:"name"`
	Item    []postmanItem   `json:"item"`
	Request *postmanRequest `json:"request,omitempty"`
}

type postmanRequest struct {
	Method string `json:"method"`
	// URL is intentionally untyped (can be string or object in v2.0 vs v2.1).
	// We do not extract it for v2 — request name carries the signal.
}

type postmanVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func mkPostmanSymbol(kind, text string, lineNum int, source, scope string, concerns []string) *Symbol {
	tags := map[string]any{
		AxisLanguage: LangJSON,
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

// isPostmanBackup filters out *.bak files that the corpus walker would
// otherwise pick up alongside *.json. Used by the per-repo dispatch
// for core-api-e2e.
func isPostmanBackup(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".bak")
}
