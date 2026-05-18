package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// decisionsYAML mirrors the on-disk shape of decisions/log.yaml — we
// only deserialize the fields we consume so missing/extra keys are
// tolerated.
type decisionsYAML struct {
	Decisions []decisionEntry `yaml:"decisions"`
}

type decisionEntry struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Date         string `yaml:"date"`
	Context      string `yaml:"context"`
	Decision     string `yaml:"decision"`
	Alternatives string `yaml:"alternatives"`
	Rationale    string `yaml:"rationale"`
	Outcome      string `yaml:"outcome"`
	Source       string `yaml:"source"`
}

// ExtractDecisions reads <kbRoot>/decisions/log.yaml and emits one
// role:domain Q&A per entry. Skips entries missing both Decision and
// Rationale — those carry no learning signal.
func ExtractDecisions(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}
	path := filepath.Join(kbRoot, "decisions", "log.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, stats, fmt.Errorf("read decisions yaml: %w", err)
	}
	stats.FilesScanned = 1

	var doc decisionsYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, stats, fmt.Errorf("parse decisions yaml: %w", err)
	}
	stats.EntriesParsed = len(doc.Decisions)

	var out []Example
	seenID := map[string]int{} // disambiguate the duplicate-ID case (D20 appears twice per meta.notes)
	for _, d := range doc.Decisions {
		if strings.TrimSpace(d.Decision) == "" && strings.TrimSpace(d.Rationale) == "" {
			continue
		}
		ex := buildDecisionExample(d, seenID)
		out = append(out, ex)
		stats.ExamplesEmitted++
		stats.PerScope[ex.Scope]++
	}
	return out, stats, nil
}

// buildDecisionExample turns one decision into a Q&A pair. The
// question is parameterized on the decision name so the model learns
// to look up by topic, not by ID. ID is preserved in metadata for
// downstream lookup.
func buildDecisionExample(d decisionEntry, seenID map[string]int) Example {
	user := fmt.Sprintf("Why did we decide on %q for ElatusDev/AkademiaPlus, and what were the alternatives?", strings.TrimSpace(d.Name))

	var b strings.Builder
	if s := strings.TrimSpace(d.Decision); s != "" {
		b.WriteString("Decision: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(d.Context); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Context: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(d.Rationale); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Rationale: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(d.Alternatives); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Alternatives considered: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(d.Outcome); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Outcome: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(d.Source); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("cite: ")
		b.WriteString(s)
	}

	id := strings.TrimSpace(d.ID)
	seenID[id]++
	source := "decisions/log.yaml#" + id
	if seenID[id] > 1 {
		source = fmt.Sprintf("decisions/log.yaml#%s-%d", id, seenID[id])
	}

	meta := map[string]string{
		"decision_id": id,
		"name":        strings.TrimSpace(d.Name),
	}
	if d.Date != "" {
		meta["date"] = strings.TrimSpace(d.Date)
	}

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: user},
			{Role: "assistant", Content: b.String()},
		},
		Tier:     1,
		Scope:    "universal",
		Source:   source,
		Role:     "domain",
		Family:   "decision-qa",
		Metadata: meta,
	}
}
