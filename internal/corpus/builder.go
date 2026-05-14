package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ResolveConfig fills defaults for any zero-valued fields.
func ResolveConfig(c Config) (Config, error) {
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)

	if c.KBRoot == "" {
		// search upwards from cwd, then alongside binary
		if found, ok := findUp("knowledge-base/README.md"); ok {
			c.KBRoot = filepath.Dir(found)
		} else if _, err := os.Stat(filepath.Join(exeDir, "..", "knowledge-base", "README.md")); err == nil {
			c.KBRoot = filepath.Join(exeDir, "..", "knowledge-base")
		}
	}
	if c.KBRoot == "" {
		return c, errors.New("--kb-root not specified and knowledge-base not found in ancestor dirs")
	}
	abs, err := filepath.Abs(c.KBRoot)
	if err != nil {
		return c, err
	}
	c.KBRoot = abs

	if c.PlatformRoot == "" {
		c.PlatformRoot = filepath.Dir(c.KBRoot)
	}
	if c.OutDir == "" {
		c.OutDir = filepath.Join(c.KBRoot, "corpus", "v1")
	}
	if c.MemoryRoot == "" {
		// $HOME/.claude/projects/-Volumes-elatusdev-ElatusDev-platform/memory
		// or platform/memory if present
		if home, _ := os.UserHomeDir(); home != "" {
			cand := filepath.Join(home, ".claude", "projects", "-Volumes-elatusdev-ElatusDev-platform", "memory")
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				c.MemoryRoot = cand
			}
		}
		if c.MemoryRoot == "" {
			cand := filepath.Join(c.PlatformRoot, "memory")
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				c.MemoryRoot = cand
			}
		}
	}
	return c, nil
}

func findUp(suffix string) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(cwd, suffix)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", false
		}
		cwd = parent
	}
}

// Build walks the configured sources and emits per-scope NDJSON + manifest.yaml.
func Build(cfg Config) error {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}

	// Load source SHAs once per repo root.
	kbSHAs, _ := gitLsFilesSHAs(cfg.KBRoot)

	// chunks accumulated per scope
	scoped := make(map[string][]Chunk, len(AllScopes))
	for _, s := range AllScopes {
		scoped[s] = nil
	}

	var sourcesMeta []SourceManifest

	// 1) Knowledge base
	err := filepath.WalkDir(cfg.KBRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(cfg.KBRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(rel))
		if !isIndexableExt(ext) {
			return nil
		}
		scope := ScopeForKBPath(rel)
		if scope == "" {
			// unmapped KB path — default to universal, but log in verbose mode
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  unmapped: %s → universal\n", rel)
			}
			scope = ScopeUniversal
		}
		docType := docTypeForPath(rel, ext)
		sha := kbSHAs[rel]

		chunks, err := chunkOne(path, rel, scope, docType, ext, sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: %s: %v\n", rel, err)
			return nil
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  %s [%s/%s] → %d chunks\n", rel, scope, docType, len(chunks))
		}
		scoped[scope] = append(scoped[scope], chunks...)
		sourcesMeta = append(sourcesMeta, SourceManifest{
			Path: rel, SHA: sha, Scope: scope, DocType: docType, Chunks: len(chunks),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk kb: %w", err)
	}

	// 2) Repo CLAUDE.md files
	repoDirs := []string{
		"core-api", "akademia-plus-web", "elatusdev-web",
		"akademia-plus-central", "akademia-plus-go",
		"core-api-e2e", "infra",
	}
	for _, rd := range repoDirs {
		claudePath := filepath.Join(cfg.PlatformRoot, rd, "CLAUDE.md")
		if _, err := os.Stat(claudePath); err != nil {
			continue
		}
		scope := ScopeForRepoClaudeMd(rd)
		if scope == "" {
			scope = ScopeUniversal
		}
		rel := filepath.ToSlash(filepath.Join(rd, "CLAUDE.md"))
		repoSHAs, _ := gitLsFilesSHAs(filepath.Join(cfg.PlatformRoot, rd))
		sha := repoSHAs["CLAUDE.md"]
		chunks, err := chunkMarkdown(claudePath, rel, scope, "claude_md", sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: %s: %v\n", rel, err)
			continue
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  %s [%s/claude_md] → %d chunks\n", rel, scope, len(chunks))
		}
		scoped[scope] = append(scoped[scope], chunks...)
		sourcesMeta = append(sourcesMeta, SourceManifest{
			Path: rel, SHA: sha, Scope: scope, DocType: "claude_md", Chunks: len(chunks),
		})
	}

	// 3) Memory (if present)
	if cfg.MemoryRoot != "" {
		_ = filepath.WalkDir(cfg.MemoryRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.ToLower(filepath.Ext(path)) != ".md" {
				return nil
			}
			base := filepath.Base(path)
			if strings.EqualFold(base, "MEMORY.md") {
				// index, not memory
				return nil
			}
			rel, _ := filepath.Rel(cfg.MemoryRoot, path)
			rel = filepath.ToSlash(filepath.Join("memory", rel))
			chunks, err := chunkMarkdown(path, rel, ScopePlatformProcess, "memory", "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warn: %s: %v\n", rel, err)
				return nil
			}
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  %s [platform-process/memory] → %d chunks\n", rel, len(chunks))
			}
			scoped[ScopePlatformProcess] = append(scoped[ScopePlatformProcess], chunks...)
			sourcesMeta = append(sourcesMeta, SourceManifest{
				Path: rel, SHA: "", Scope: ScopePlatformProcess, DocType: "memory", Chunks: len(chunks),
			})
			return nil
		})
	}

	// Compute inbound citations across the union of all chunks
	all := make([]*Chunk, 0)
	idIndex := make(map[string][]*Chunk) // artifact_id → chunks that define/own it
	for scope, cs := range scoped {
		for i := range cs {
			all = append(all, &scoped[scope][i])
			if cs[i].ArtifactID != "" {
				idIndex[cs[i].ArtifactID] = append(idIndex[cs[i].ArtifactID], &scoped[scope][i])
			}
		}
	}
	// invert cites_outbound into cites_inbound on the owning chunks
	for _, c := range all {
		for _, cite := range c.Metadata.CitesOutbound {
			if owners, ok := idIndex[cite]; ok {
				for _, owner := range owners {
					if owner == c {
						continue
					}
					owner.Metadata.CitesInbound = appendUnique(owner.Metadata.CitesInbound, c.ArtifactID)
				}
			}
		}
	}

	// Sort each scope deterministically, then write
	byScope := make(map[string]int, len(AllScopes))
	byDocType := make(map[string]int)
	total := 0
	for _, scope := range AllScopes {
		cs := scoped[scope]
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].Source != cs[j].Source {
				return cs[i].Source < cs[j].Source
			}
			return cs[i].SourceAnchor < cs[j].SourceAnchor
		})
		outPath := filepath.Join(cfg.OutDir, scope+".ndjson")
		if err := writeNDJSON(outPath, cs); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		byScope[scope] = len(cs)
		for _, c := range cs {
			byDocType[c.DocType]++
		}
		total += len(cs)
	}

	// Manifest
	sort.Slice(sourcesMeta, func(i, j int) bool { return sourcesMeta[i].Path < sourcesMeta[j].Path })
	manifest := Manifest{
		BuiltAt:        nowISO(),
		BuilderVersion: BuilderVersion,
		TotalChunks:    total,
		ByScope:        byScope,
		ByDocType:      byDocType,
		Sources:        sourcesMeta,
	}
	manifestPath := filepath.Join(cfg.OutDir, "manifest.yaml")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Summary
	fmt.Printf("corpus v1 built at %s\n", cfg.OutDir)
	fmt.Printf("  total chunks: %d across %d sources\n", total, len(sourcesMeta))
	for _, scope := range AllScopes {
		fmt.Printf("  %-18s %d chunks\n", scope, byScope[scope])
	}
	return nil
}

// chunkOne dispatches to the right chunker based on extension and source kind.
func chunkOne(absPath, rel, scope, docType, ext, sha string) ([]Chunk, error) {
	if ext == ".yaml" && isStructuredYAML(rel) {
		return chunkYAML(absPath, rel, scope, docType, sha)
	}
	if ext == ".md" {
		return chunkMarkdown(absPath, rel, scope, docType, sha)
	}
	return nil, nil
}

// isStructuredYAML — only the canonical catalogs are chunked as YAML.
// Other YAMLs (e.g., game_central.params.json, validate.py, future configs) are
// either non-YAML or process-side and not part of the v1 corpus.
func isStructuredYAML(rel string) bool {
	p := filepath.ToSlash(rel)
	switch {
	case strings.HasPrefix(p, "standards/") && strings.HasSuffix(p, ".yaml"):
		return true
	case p == "decisions/log.yaml":
		return true
	case p == "anti-patterns/catalog.yaml":
		return true
	case strings.HasPrefix(p, "dictionary/") && strings.HasSuffix(p, ".yaml"):
		return true
	}
	return false
}

func isIndexableExt(ext string) bool {
	return ext == ".md" || ext == ".yaml"
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", "build", ".idea", ".vscode":
		return true
	case "v1": // corpus output dir — never re-index ourselves
		return true
	}
	return false
}

func writeNDJSON(path string, chunks []Chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for i := range chunks {
		if err := enc.Encode(&chunks[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeManifest(path string, m Manifest) error {
	out, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func appendUnique(s []string, v string) []string {
	if v == "" {
		return s
	}
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
