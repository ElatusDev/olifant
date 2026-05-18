package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// failureModesYAML mirrors the on-disk shape of the curated
// failure-mode source (kb-root/eval/failure-modes/v*.yaml). Only the
// fields the emitter consumes are deserialized.
type failureModesYAML struct {
	Meta struct {
		Version int    `yaml:"version"`
		Source  string `yaml:"source"`
	} `yaml:"meta"`
	Entries []failureModeEntry `yaml:"entries"`
}

type failureModeEntry struct {
	ID                       string `yaml:"id"`
	Code                     string `yaml:"code"`
	Scope                    string `yaml:"scope"`
	WhatTheModelDoesWrong    string `yaml:"what_the_model_does_wrong"`
	UserPrompt               string `yaml:"user_prompt"`
	CorrectAssistantResponse string `yaml:"correct_assistant_response"`
	Rationale                string `yaml:"rationale"`
	Cite                     string `yaml:"cite"`
}

// failureModesDir is where we look for curated source files. We pick
// the highest-numbered v<N>.yaml so the active dataset always
// consumes the latest authored corrections; older versions sit
// alongside as historical reference for reproducible older training
// runs.
const failureModesDir = "eval/failure-modes"

// ExtractFailureModes loads <kbRoot>/eval/failure-modes/v<N>.yaml
// (highest N) and emits one role:domain example per entry. The
// example shape is intentionally minimal: user_prompt → correct
// response. The bad form is referenced only in the entry's metadata
// for human auditability and is NOT injected into the training set —
// the model learns the right answer by repetition, not by being
// shown the wrong answer.
func ExtractFailureModes(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}
	dir := filepath.Join(kbRoot, failureModesDir)
	path, err := pickLatestFailureModesFile(dir)
	if err != nil {
		// Missing dir is fine — failure-modes source is optional.
		if os.IsNotExist(err) {
			return nil, stats, nil
		}
		return nil, stats, err
	}
	if path == "" {
		// Directory exists but no v*.yaml — also fine.
		return nil, stats, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, stats, fmt.Errorf("read failure-modes yaml: %w", err)
	}
	stats.FilesScanned = 1

	var doc failureModesYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, stats, fmt.Errorf("parse failure-modes yaml: %w", err)
	}
	stats.EntriesParsed = len(doc.Entries)

	relPath, _ := filepath.Rel(kbRoot, path)
	relPath = filepath.ToSlash(relPath)

	var out []Example
	for _, fm := range doc.Entries {
		if strings.TrimSpace(fm.UserPrompt) == "" || strings.TrimSpace(fm.CorrectAssistantResponse) == "" {
			continue
		}
		ex := buildFailureModeExample(relPath, fm)
		out = append(out, ex)
		stats.ExamplesEmitted++
		stats.PerScope[ex.Scope]++
	}
	return out, stats, nil
}

// pickLatestFailureModesFile returns the lex-greatest `v<N>.yaml` in
// the dir, where <N> is a positive integer. Non-matching files are
// ignored. Returns ("", nil) for empty dir, error only on real I/O
// failure.
func pickLatestFailureModesFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "v") && strings.HasSuffix(n, ".yaml") {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Strings(candidates)
	return filepath.Join(dir, candidates[len(candidates)-1]), nil
}

func buildFailureModeExample(sourcePath string, fm failureModeEntry) Example {
	scope := strings.TrimSpace(fm.Scope)
	if scope == "" {
		scope = "universal"
	}

	meta := map[string]string{
		"failure_mode_id": strings.TrimSpace(fm.ID),
		"code":            strings.TrimSpace(fm.Code),
	}
	if s := strings.TrimSpace(fm.WhatTheModelDoesWrong); s != "" {
		meta["bad_form_documented"] = "true"
	}
	if s := strings.TrimSpace(fm.Cite); s != "" {
		meta["origin_cite"] = s
	}
	if s := strings.TrimSpace(fm.Rationale); s != "" {
		// Rationale is included as metadata for human review; we
		// don't append it to the assistant turn because the training
		// signal we want is "produce the correct response," not
		// "produce the correct response and explain why."
		meta["rationale"] = s
	}

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: strings.TrimSpace(fm.UserPrompt)},
			{Role: "assistant", Content: strings.TrimSpace(fm.CorrectAssistantResponse)},
		},
		Tier:     1,
		Scope:    scope,
		Source:   sourcePath + "#" + strings.TrimSpace(fm.ID),
		Role:     "domain",
		Family:   "failure-mode-correction",
		Metadata: meta,
	}
}
