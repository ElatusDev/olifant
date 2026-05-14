// Package dictionary manages the Olifant CNL v1 dictionaries described in
// platform/knowledge-base/dsl/cnl-v1.md.
package dictionary

// Entry matches the schema in cnl-v1.md "Entry schema (all dictionary files)".
type Entry struct {
	Term                string   `yaml:"term"`
	Category            string   `yaml:"category"`
	Synonyms            []string `yaml:"synonyms,omitempty"`
	Definition          string   `yaml:"definition"`
	Cites               []string `yaml:"cites"`
	Related             []string `yaml:"related,omitempty"`
	Introduced          string   `yaml:"introduced"`
	IntroducedBy        string   `yaml:"introduced_by"`
	IntroducedInRequest string   `yaml:"introduced_in_request,omitempty"`
	Deprecated          string   `yaml:"deprecated,omitempty"`
	SupersededBy        string   `yaml:"superseded_by,omitempty"`
}

// Scope mirrors the corpus scope set.
const (
	ScopeUniversal       = "universal"
	ScopeBackend         = "backend"
	ScopeWebapp          = "webapp"
	ScopeMobile          = "mobile"
	ScopeE2E             = "e2e"
	ScopeInfra           = "infra"
	ScopePlatformProcess = "platform-process"
)
