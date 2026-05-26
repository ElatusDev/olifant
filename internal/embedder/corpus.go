// Package embedder hosts the Phase B (domain embedder) pipeline:
// corpus loading, hard-negative mining, and triple generation
// (anchor / positive paraphrase / hard negative) for training a
// 768-d sentence-transformer to replace nomic-embed-text in
// olifant's RAG retrieval path.
//
// The training itself lives in a separate Modal Python script
// (per `feedback_olifant_uses_claude_code_only.md` and the
// Phase B prompt's §1 HARD RULE #2: tooling-in-Go except for
// the embedder-training subprocess).
package embedder

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentence is one entry from a v2-curriculum prose YAML — a sentence
// extracted from the platform corpus, with its multi-axis tags from
// the Day-5 prose tokenizer + classifier.
type Sentence struct {
	ID           string   `yaml:"id"`
	Text         string   `yaml:"text"`
	Source       string   `yaml:"source"`
	Line         int      `yaml:"line"`
	Tags         RawTags  `yaml:"tags"`
	Scope        string   `yaml:"-"`
	SemanticRole string   `yaml:"-"`
	Concerns     []string `yaml:"-"`
	Language     string   `yaml:"-"`
}

// RawTags is the on-disk tag block. Concern may be a string OR a list,
// since classifier emits both shapes; we normalise into Sentence.Concerns
// via populateAxes after Unmarshal.
type RawTags struct {
	Concern       yaml.Node `yaml:"concern"`
	Language      string    `yaml:"language"`
	Scope         string    `yaml:"scope"`
	SemanticRole  string    `yaml:"semantic_role"`
	SyntacticForm string    `yaml:"syntactic_form"`
}

// LoadProse walks proseDir, parses every *.yaml file at any depth, and
// returns sentences with axes flattened from Tags. Returns deterministic
// order (sorted by source path + line).
func LoadProse(proseDir string) ([]Sentence, error) {
	var paths []string
	err := filepath.Walk(proseDir, func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", proseDir, err)
	}
	sort.Strings(paths)

	var all []Sentence
	for _, p := range paths {
		ss, perr := loadProseFile(p)
		if perr != nil {
			return nil, fmt.Errorf("load %s: %w", p, perr)
		}
		all = append(all, ss...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Source != all[j].Source {
			return all[i].Source < all[j].Source
		}
		return all[i].Line < all[j].Line
	})
	return all, nil
}

func loadProseFile(path string) ([]Sentence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []Sentence
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, s := range entries {
		s.populateAxes()
		if s.Text == "" || s.ID == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// populateAxes flattens RawTags into the Sentence axis fields. Concern
// is normalised from either a YAML string or sequence into []string;
// empty/missing axes become "" / nil.
func (s *Sentence) populateAxes() {
	s.Scope = strings.TrimSpace(s.Tags.Scope)
	s.SemanticRole = strings.TrimSpace(s.Tags.SemanticRole)
	s.Language = strings.TrimSpace(s.Tags.Language)
	s.Concerns = s.Concerns[:0]

	switch s.Tags.Concern.Kind {
	case yaml.ScalarNode:
		v := strings.TrimSpace(s.Tags.Concern.Value)
		if v != "" {
			s.Concerns = append(s.Concerns, v)
		}
	case yaml.SequenceNode:
		for _, n := range s.Tags.Concern.Content {
			v := strings.TrimSpace(n.Value)
			if v != "" {
				s.Concerns = append(s.Concerns, v)
			}
		}
	}
}

// ScopeIndex groups sentences by their Scope. Useful for negative mining
// (Phase B §4 B1a step (c): "same scope, different semantic_role").
func ScopeIndex(sentences []Sentence) map[string][]int {
	out := map[string][]int{}
	for i, s := range sentences {
		if s.Scope == "" {
			continue
		}
		out[s.Scope] = append(out[s.Scope], i)
	}
	return out
}
