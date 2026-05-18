package dataset

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Per-section excerpt cap for triple emission — bounds example size
// while preserving enough content for the model to see "design →
// execution → reflection" coupling.
const tripleExcerptCap = 800

// triplePathSuffixes are the three file naming conventions that
// define a lifecycle triple.
var triplePathSuffixes = []string{"-workflow.md", "-prompt.md", "-retrospective.md"}

// triplePathLabels for the assistant-side rendering.
var triplePathLabels = map[string]string{
	"-workflow.md":      "Workflow (design)",
	"-prompt.md":        "Prompt (execution)",
	"-retrospective.md": "Retrospective (outcome)",
}

// triplePathDirs maps the suffix to its kb root directory.
var triplePathDirs = map[string]string{
	"-workflow.md":      "workflows",
	"-prompt.md":        "prompts",
	"-retrospective.md": "retrospectives",
}

// tripleKey identifies one lifecycle stem under one project.
type tripleKey struct {
	Project string
	Stem    string
}

// stemPaths collects the three artifact paths (relative to kb-root)
// for one stem under one project. Any of the three may be empty if
// the corresponding artifact does not exist.
type stemPaths struct {
	Workflow string
	Prompt   string
	Retro    string
}

// ExtractTriples walks workflows/, prompts/, retrospectives/ under
// kbRoot, matches files by (project, stem), and emits one
// role:prompt_build example per stem that has all three artifacts.
// Stems missing any of the three are skipped — partial coverage is
// noise for prompt-build training.
func ExtractTriples(kbRoot string) ([]Example, SourceStats, error) {
	stats := SourceStats{PerScope: map[string]int{}}

	index := map[tripleKey]*stemPaths{}
	for suffix, sub := range triplePathDirs {
		dir := filepath.Join(kbRoot, sub)
		count, err := indexTripleDir(dir, kbRoot, suffix, index)
		if err != nil {
			return nil, stats, err
		}
		stats.FilesScanned += count
	}
	stats.EntriesParsed = len(index)

	// Deterministic emission order: project, then stem.
	keys := make([]tripleKey, 0, len(index))
	for k := range index {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Project != keys[j].Project {
			return keys[i].Project < keys[j].Project
		}
		return keys[i].Stem < keys[j].Stem
	})

	var out []Example
	for _, k := range keys {
		paths := index[k]
		if paths.Workflow == "" || paths.Prompt == "" || paths.Retro == "" {
			continue
		}
		scope, ok := retroScopeByProject[k.Project]
		if !ok {
			// Same scope map as retros — keep them consistent.
			continue
		}
		ex, err := buildTripleExample(kbRoot, k, scope, paths)
		if err != nil {
			return nil, stats, err
		}
		out = append(out, ex)
		stats.ExamplesEmitted++
		stats.PerScope[scope]++
	}
	return out, stats, nil
}

// indexTripleDir walks a single root (workflows/ or prompts/ or
// retrospectives/) and records each matching file in `index` under
// its (project, stem) key. Files at the top level of the root that
// don't sit under a project subdir are skipped — those aren't part
// of the per-project lifecycle convention.
func indexTripleDir(root, kbRoot, suffix string, index map[tripleKey]*stemPaths) (int, error) {
	var count int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil // missing root is fine
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, suffix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return nil // top-level file: no project dir, skip
		}
		project := parts[0]
		stem := strings.TrimSuffix(name, suffix)

		key := tripleKey{Project: project, Stem: stem}
		ent, ok := index[key]
		if !ok {
			ent = &stemPaths{}
			index[key] = ent
		}
		// Relative-to-kbRoot path for the metadata + Source field.
		relFromKB, err := filepath.Rel(kbRoot, path)
		if err != nil {
			return err
		}
		switch suffix {
		case "-workflow.md":
			ent.Workflow = filepath.ToSlash(relFromKB)
		case "-prompt.md":
			ent.Prompt = filepath.ToSlash(relFromKB)
		case "-retrospective.md":
			ent.Retro = filepath.ToSlash(relFromKB)
		}
		count++
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return count, fmt.Errorf("walk %s: %w", root, err)
	}
	return count, nil
}

// buildTripleExample produces a single role:prompt_build training row
// pairing the three artifacts. The assistant content is a structured
// digest: title + excerpt from each of workflow, prompt, retro, with
// each excerpt capped at tripleExcerptCap.
func buildTripleExample(kbRoot string, k tripleKey, scope string, paths *stemPaths) (Example, error) {
	user := fmt.Sprintf(
		"For ElatusDev/AkademiaPlus, show how the %q work cycle in %s progressed from design to execution to outcome.",
		k.Stem, k.Project,
	)

	// Read each file's first H1 + excerpt.
	wfDigest, err := digestArtifact(filepath.Join(kbRoot, paths.Workflow))
	if err != nil {
		return Example{}, err
	}
	ptDigest, err := digestArtifact(filepath.Join(kbRoot, paths.Prompt))
	if err != nil {
		return Example{}, err
	}
	rtDigest, err := digestArtifact(filepath.Join(kbRoot, paths.Retro))
	if err != nil {
		return Example{}, err
	}

	var b strings.Builder
	for _, sec := range []struct {
		Suffix string
		Path   string
		Dig    artifactDigest
	}{
		{"-workflow.md", paths.Workflow, wfDigest},
		{"-prompt.md", paths.Prompt, ptDigest},
		{"-retrospective.md", paths.Retro, rtDigest},
	} {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## ")
		b.WriteString(triplePathLabels[sec.Suffix])
		b.WriteString("\n")
		if sec.Dig.Title != "" {
			b.WriteString("Title: ")
			b.WriteString(sec.Dig.Title)
			b.WriteString("\n")
		}
		if sec.Dig.Excerpt != "" {
			b.WriteString("Excerpt: ")
			b.WriteString(sec.Dig.Excerpt)
			b.WriteString("\n")
		}
		b.WriteString("cite: ")
		b.WriteString(sec.Path)
	}

	return Example{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: user},
			{Role: "assistant", Content: b.String()},
		},
		Tier:   2,
		Scope:  scope,
		Source: "lifecycle/" + k.Project + "/" + k.Stem,
		Role:   "prompt_build",
		Family: "lifecycle-triple",
		Metadata: map[string]string{
			"project":      k.Project,
			"stem":         k.Stem,
			"workflow":     paths.Workflow,
			"prompt":       paths.Prompt,
			"retrospective": paths.Retro,
		},
	}, nil
}

type artifactDigest struct {
	Title   string // first H1 line, "# " stripped
	Excerpt string // first non-empty, non-heading paragraph, capped at tripleExcerptCap chars
}

// digestArtifact reads `path` and returns its first H1 title plus the
// first body paragraph, capped. Cheap one-shot read; these files are
// typically <50KB.
func digestArtifact(path string) (artifactDigest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return artifactDigest{}, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	var (
		title    string
		excerpt  strings.Builder
		inFence  bool
	)
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if title == "" && strings.HasPrefix(ln, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") {
			if excerpt.Len() > 0 {
				break
			}
			continue
		}
		if trimmed == "" {
			if excerpt.Len() > 0 {
				break
			}
			continue
		}
		if excerpt.Len() > 0 {
			excerpt.WriteByte(' ')
		}
		excerpt.WriteString(trimmed)
		if excerpt.Len() >= tripleExcerptCap {
			break
		}
	}
	out := strings.TrimSpace(excerpt.String())
	if len(out) > tripleExcerptCap {
		out = out[:tripleExcerptCap] + "…"
	}
	return artifactDigest{Title: title, Excerpt: out}, nil
}
