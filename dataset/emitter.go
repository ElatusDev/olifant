package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// emitJSONL appends examples to <outDir>/<basename>.jsonl, grouped by
// scope. Returns total rows written. Empty scope groups under
// "universal.jsonl" so cross-cutting Tier 1 sources (decisions,
// anti-patterns) land in one file.
func emitJSONL(outDir string, examples []Example) (rows int, perScope map[string]int, err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, nil, fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	perScope = map[string]int{}

	// Group by scope so we open each file once.
	byScope := map[string][]Example{}
	for _, ex := range examples {
		scope := ex.Scope
		if scope == "" {
			scope = "universal"
		}
		byScope[scope] = append(byScope[scope], ex)
	}

	// Deterministic file ordering for reproducible manifests.
	scopes := make([]string, 0, len(byScope))
	for s := range byScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	for _, scope := range scopes {
		fpath := filepath.Join(outDir, scope+".jsonl")
		f, err := os.Create(fpath)
		if err != nil {
			return rows, perScope, fmt.Errorf("create %s: %w", fpath, err)
		}
		enc := json.NewEncoder(f)
		enc.SetEscapeHTML(false)
		for _, ex := range byScope[scope] {
			if err := enc.Encode(&ex); err != nil {
				f.Close()
				return rows, perScope, fmt.Errorf("encode example %s: %w", ex.Source, err)
			}
			rows++
			perScope[scope]++
		}
		f.Close()
	}
	return rows, perScope, nil
}

// writeManifest persists the run manifest as YAML.
func writeManifest(outDir string, m *Manifest) error {
	mpath := filepath.Join(outDir, "manifest.yaml")
	f, err := os.Create(mpath)
	if err != nil {
		return fmt.Errorf("create %s: %w", mpath, err)
	}
	defer f.Close()
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close manifest encoder: %w", err)
	}
	return nil
}

// nowUTC is overridable by tests for deterministic manifests.
var nowUTC = func() time.Time { return time.Now().UTC() }
