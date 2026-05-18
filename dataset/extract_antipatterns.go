package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// antipatternsYAML mirrors the on-disk shape of
// anti-patterns/catalog.yaml. Top-level key is `entries`.
type antipatternsYAML struct {
	Entries []antipatternEntry `yaml:"entries"`
}

type antipatternEntry struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	Context       string `yaml:"context"`
	WhatHappened  string `yaml:"what_happened"`
	RootCause     string `yaml:"root_cause"`
	Alternative   string `yaml:"alternative"`
	Source        string `yaml:"source"`
}

// ExtractAntipatterns reads <kbRoot>/anti-patterns/catalog.yaml and
// emits one role:challenge example per entry — the assistant rejects
// the anti-pattern as INVALID, citing root cause + the correct
// alternative. Entries without a name are skipped.
func ExtractAntipatterns(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}
	path := filepath.Join(kbRoot, "anti-patterns", "catalog.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, stats, fmt.Errorf("read antipatterns yaml: %w", err)
	}
	stats.FilesScanned = 1

	var doc antipatternsYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, stats, fmt.Errorf("parse antipatterns yaml: %w", err)
	}
	stats.EntriesParsed = len(doc.Entries)

	var out []Example
	for _, ap := range doc.Entries {
		if strings.TrimSpace(ap.Name) == "" {
			continue
		}
		ex := buildAntipatternExample(ap)
		out = append(out, ex)
		stats.ExamplesEmitted++
		stats.PerScope[ex.Scope]++
	}
	return out, stats, nil
}

func buildAntipatternExample(ap antipatternEntry) Example {
	name := strings.TrimSpace(ap.Name)
	ctx := strings.TrimSpace(ap.Context)

	var user strings.Builder
	if ctx != "" {
		fmt.Fprintf(&user, "Context: %s. ", ctx)
	}
	fmt.Fprintf(&user, "Would it be ok to do %q here?", name)

	var b strings.Builder
	b.WriteString("verdict: INVALID")
	if id := strings.TrimSpace(ap.ID); id != "" {
		b.WriteString(" (")
		b.WriteString(id)
		b.WriteString(")")
	}
	b.WriteString("\n\nThis is a known anti-pattern: ")
	b.WriteString(name)
	b.WriteString(".")

	if s := strings.TrimSpace(ap.WhatHappened); s != "" {
		b.WriteString("\n\nWhat goes wrong: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(ap.RootCause); s != "" {
		b.WriteString("\n\nRoot cause: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(ap.Alternative); s != "" {
		b.WriteString("\n\nUse instead: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(ap.Source); s != "" {
		b.WriteString("\n\ncite: ")
		b.WriteString(s)
	}

	meta := map[string]string{"antipattern_id": strings.TrimSpace(ap.ID), "name": name}

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: user.String()},
			{Role: "assistant", Content: b.String()},
		},
		Tier:     1,
		Scope:    "universal",
		Source:   "anti-patterns/catalog.yaml#" + strings.TrimSpace(ap.ID),
		Role:     "challenge",
		Family:   "antipattern-challenge",
		Metadata: meta,
	}
}
