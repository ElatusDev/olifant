package corpus

import (
	"path/filepath"
	"strings"
	"testing"
)

// AC4 probe (epic #119 S1, workflow D-2/AP184): a populated response-cache
// store must contribute ZERO corpus sources — cached model output must never
// become retrievable truth. The store's default root (~/.olifant) is outside
// every walk root by construction; this probe additionally pins the
// defense-in-depth case of a cache tree nested INSIDE a walked root (e.g. a
// misconfigured OLIFANT_RESPONSE_CACHE_DIR): the `.olifant` dir is skipped
// at directory level, so even indexable-extension files inside it stay out.
func TestBuildCorpus_ResponseCacheNeverIndexed(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, "kb")
	writeFile(t, filepath.Join(kb, "patterns", "backend.md"), "# P\n\n## X\n\nbody\n")

	// Populated cache store nested inside the KB root — worst placement.
	cache := filepath.Join(kb, ".olifant", "responses")
	writeFile(t, filepath.Join(cache, "objects", "ab", "abc123.json"), `{"payload":{"raw":"model output"}}`)
	writeFile(t, filepath.Join(cache, "log.ndjson"), `{"event":"store"}`)
	// Even an indexable-extension file inside the cache tree must be skipped
	// (dir-level skip, not extension filtering).
	writeFile(t, filepath.Join(cache, "notes.md"), "# model output masquerading as a doc\n")

	// And one in the memory root, the other walked tree.
	mem := filepath.Join(root, "memory")
	writeFile(t, filepath.Join(mem, "note.md"), "# Note\n\nremember\n")
	writeFile(t, filepath.Join(mem, ".olifant", "responses", "objects", "cd", "def456.json"), `{}`)

	cfg, err := ResolveConfig(Config{
		KBRoot: kb, PlatformRoot: root,
		OutDir: filepath.Join(root, "out"), MemoryRoot: mem,
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	_, m, err := buildCorpus(cfg)
	if err != nil {
		t.Fatalf("buildCorpus: %v", err)
	}
	for _, src := range m.Sources {
		if strings.Contains(src.Path, ".olifant") {
			t.Errorf("response-cache file indexed as corpus source: %s (AC4 violated)", src.Path)
		}
	}
	if len(m.Sources) != 2 {
		t.Errorf("sources = %d (%+v), want exactly the pattern doc + memory note", len(m.Sources), m.Sources)
	}
}
