package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_EndToEnd(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	out := filepath.Join(root, "out")
	mem := filepath.Join(root, "memory")

	// KB sources across several scopes + doc types.
	writeFile(t, filepath.Join(kb, "decisions", "log.yaml"), `- id: D1
  title: First decision
- id: D2
  title: Second decision
  supersedes: D1
`)
	writeFile(t, filepath.Join(kb, "patterns", "backend.md"), "# Backend Patterns\n\n## Caching\n\nSee D1 for rationale.\n")
	writeFile(t, filepath.Join(kb, "architecture", "overview.md"), "# Arch\n\nflat doc\n")
	writeFile(t, filepath.Join(kb, "unmapped", "weird.md"), "# Weird\n\nunmapped path defaults to universal\n")
	// Non-indexable file is ignored.
	writeFile(t, filepath.Join(kb, "patterns", "ignore.txt"), "not indexed")
	// Skipped dir.
	writeFile(t, filepath.Join(kb, "node_modules", "junk.md"), "# junk\n")

	// Repo CLAUDE.md.
	writeFile(t, filepath.Join(root, "core-api", "CLAUDE.md"), "# core-api\n\n## Build\n\nmvn test\n")

	// Memory: one real note + the MEMORY.md index (which must be skipped).
	writeFile(t, filepath.Join(mem, "note-a.md"), "# Note A\n\nremember this\n")
	writeFile(t, filepath.Join(mem, "MEMORY.md"), "# Index\n\n- pointer\n")

	cfg := Config{KBRoot: kb, PlatformRoot: root, OutDir: out, MemoryRoot: mem}
	if err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Manifest written.
	if _, err := os.Stat(filepath.Join(out, "manifest.yaml")); err != nil {
		t.Errorf("manifest.yaml not written: %v", err)
	}

	// One NDJSON per scope exists (AllScopes), and universal carries the decisions.
	uni, err := os.ReadFile(filepath.Join(out, ScopeUniversal+".ndjson"))
	if err != nil {
		t.Fatalf("read universal.ndjson: %v", err)
	}
	uniStr := string(uni)
	if !strings.Contains(uniStr, `"artifact_id":"D1"`) {
		t.Errorf("universal scope missing D1:\n%s", uniStr)
	}
	// Inbound-cite inversion: D1 is cited by D2 → D1 chunk gets cites_inbound D2.
	if !strings.Contains(uniStr, `"cites_inbound":["D2"]`) {
		t.Errorf("inbound-cite inversion missing (D1 should be cited by D2):\n%s", uniStr)
	}

	// platform-process scope captured the memory note (but not MEMORY.md).
	pp, _ := os.ReadFile(filepath.Join(out, ScopePlatformProcess+".ndjson"))
	if !strings.Contains(string(pp), "memory/note-a.md") {
		t.Errorf("memory note not indexed:\n%s", pp)
	}
	if strings.Contains(string(pp), "MEMORY.md") {
		t.Error("MEMORY.md index should be skipped")
	}

	// backend scope captured the repo CLAUDE.md (claude_md doc type).
	be, _ := os.ReadFile(filepath.Join(out, ScopeBackend+".ndjson"))
	if !strings.Contains(string(be), "core-api/CLAUDE.md") {
		t.Errorf("repo CLAUDE.md not indexed into backend scope:\n%s", be)
	}
}

func TestBuild_MkdirOutDirError(t *testing.T) {
	// OutDir whose parent is a regular file → MkdirAll fails.
	f := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	err := Build(Config{KBRoot: t.TempDir(), PlatformRoot: t.TempDir(), OutDir: filepath.Join(f, "out")})
	if err == nil {
		t.Error("Build with un-creatable OutDir should error")
	}
}
