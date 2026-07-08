package digest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/promptgate"
	"github.com/ElatusDev/olifant/internal/synth"
)

// fakeSynth returns canned responses in order, then repeats the last.
type fakeSynth struct {
	responses []string
	calls     int
}

func (f *fakeSynth) Generate(_ context.Context, _ synth.Request) (*synth.Response, error) {
	i := f.calls
	if i >= len(f.responses) {
		i = len(f.responses) - 1
	}
	f.calls++
	return &synth.Response{Text: f.responses[i]}, nil
}

// fixture builds a minimal platform tree the promptgate resolver can scan,
// plus a digestible source doc, and returns everything a Config needs.
func fixture(t *testing.T) (cfg Config, kbRoot string) {
	t.Helper()
	platformRoot := t.TempDir()
	kbRoot = filepath.Join(platformRoot, "knowledge-base")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(kbRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("decisions/log.md", "### D210 — the ratified thing\n")
	write("anti-patterns/catalog.md", "### AP164 — the trap\n")
	write("dictionary/universal/domain.yaml", "- term: sample\n")
	write("corpus/v1/manifest.yaml", "sources:\n  - path: decisions/log.md\n    sha: aaa\n    scope: universal\n    doc_type: decision\n    chunks: 1\n")
	write("architecture/big-doc.md", strings.Repeat("Design prose grounded in D210 and AP164.\n", 40))

	r, err := promptgate.NewResolver(platformRoot, kbRoot)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return Config{
		SourcePath: filepath.Join(kbRoot, "architecture", "big-doc.md"),
		SourceRel:  "knowledge-base/architecture/big-doc.md",
		CacheDir:   filepath.Join(t.TempDir(), "digests"),
		Model:      "test-model",
		Resolver:   r,
	}, kbRoot
}

const goodBody = "The big doc describes the design.\n\n- Grounded in D210 (the ratified thing).\n- Avoids AP164.\n- Everything else is prose detail an engineer can pull on demand.\n"

func TestRun_CleanDigestCachedAndEmitted(t *testing.T) {
	cfg, _ := fixture(t)
	fs := &fakeSynth{responses: []string{goodBody}}
	cfg.Synth = fs

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CacheHit || res.Attempts != 1 {
		t.Errorf("first run: cacheHit=%v attempts=%d", res.CacheHit, res.Attempts)
	}
	if !strings.Contains(res.Digest, "source: knowledge-base/architecture/big-doc.md") ||
		!strings.Contains(res.Digest, "source_sha: "+res.SourceSHA) {
		t.Errorf("footer provenance missing:\n%s", res.Digest)
	}
	if res.Ratio <= 0 || res.Ratio >= 1 {
		t.Errorf("ratio = %v, want (0,1) for a compressing digest", res.Ratio)
	}
	if _, err := os.Stat(res.CachePath); err != nil {
		t.Errorf("digest not cached: %v", err)
	}

	// Second run: cache hit, zero synth calls.
	res2, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run(cached): %v", err)
	}
	if !res2.CacheHit || fs.calls != 1 {
		t.Errorf("cache hit expected with no new synth call: hit=%v calls=%d", res2.CacheHit, fs.calls)
	}

	// -refresh regenerates.
	cfg.Refresh = true
	res3, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run(refresh): %v", err)
	}
	if res3.CacheHit || fs.calls != 2 {
		t.Errorf("refresh must regenerate: hit=%v calls=%d", res3.CacheHit, fs.calls)
	}
}

func TestRun_UnresolvedCiteRetriesThenFailsHonestly(t *testing.T) {
	cfg, _ := fixture(t)
	bad := "Summary that fabricates D9999 as its basis.\n" + strings.Repeat("More prose to clear the size floor. ", 10)
	cfg.Synth = &fakeSynth{responses: []string{bad, bad}}

	_, err := Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "D9999") {
		t.Fatalf("want honest failure naming D9999, got %v", err)
	}
	// Nothing cached (D-DG2).
	entries, _ := os.ReadDir(cfg.CacheDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("invalid digest reached the cache: %s", e.Name())
		}
	}
}

func TestRun_RetryRecoversAfterFeedback(t *testing.T) {
	cfg, _ := fixture(t)
	bad := "Fabricates AP9999.\n" + strings.Repeat("Padding prose for the size floor. ", 10)
	fs := &fakeSynth{responses: []string{bad, goodBody}}
	cfg.Synth = fs

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Attempts != 2 || fs.calls != 2 {
		t.Errorf("attempts=%d calls=%d, want 2/2", res.Attempts, fs.calls)
	}
}

func TestRun_DegenerateBodyRetried(t *testing.T) {
	cfg, _ := fixture(t)
	fs := &fakeSynth{responses: []string{"too short", goodBody}}
	cfg.Synth = fs

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (degenerate first attempt)", res.Attempts)
	}
}

func TestRun_GuardsAndCaps(t *testing.T) {
	cfg, kbRoot := fixture(t)

	// Missing synth/resolver on a cache miss = hard error (code judges).
	noSynth := cfg
	noSynth.Synth = nil
	if _, err := Run(context.Background(), noSynth); err == nil {
		t.Error("nil synth must error, not emit an unvalidated digest")
	}

	// Oversized source refuses (no silent truncation).
	big := filepath.Join(kbRoot, "architecture", "huge.md")
	if err := os.WriteFile(big, make([]byte, maxInputBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	over := cfg
	over.SourcePath = big
	over.SourceRel = "knowledge-base/architecture/huge.md"
	over.Synth = &fakeSynth{responses: []string{goodBody}}
	if _, err := Run(context.Background(), over); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("oversized source must refuse, got %v", err)
	}

	// The cache never writes under the KB root (D-DG3 structural).
	cfg.Synth = &fakeSynth{responses: []string{goodBody}}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(res.CachePath, kbRoot) {
		t.Errorf("cache path %s is under the KB root", res.CachePath)
	}
}
