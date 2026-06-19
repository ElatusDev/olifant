package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// indexServers returns explicit Ollama + Chroma URLs for commands that take
// -ollama-url / -chroma-url flags (history index, dataset index).
func indexServers(t *testing.T) (oURL, cURL string) {
	t.Helper()
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.5.0"}`))
		case "/api/embed":
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(oll.Close)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_, _ = w.Write([]byte(`{"nanosecond heartbeat":1}`))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"c1","name":"x"}`))
		case strings.HasSuffix(r.URL.Path, "/count"):
			_, _ = w.Write([]byte(`0`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)
	return oll.URL, chr.URL
}

func TestTruncate(t *testing.T) {
	if got := truncate("  hi  ", 10); got != "hi" {
		t.Errorf("truncate trim = %q", got)
	}
	got := truncate(strings.Repeat("a", 50), 5)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate long = %q", got)
	}
}

func TestCorpusStats_Stub(t *testing.T) {
	if code := corpusStats(nil); code != 1 {
		t.Errorf("corpusStats = %d, want 1 (stub)", code)
	}
}

func TestPlanSplit_OverCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("plan_id: 2026-06-19T08-05-03Z-split1\ngoal: big\nsteps:\n")
	for i := 1; i <= 30; i++ { // > MaxStepsPerPlan → writes sub-plans
		fmt.Fprintf(&b, "  - id: step_%02d\n    description: do step\n    expected_output:\n      schema:\n        type: object\n", i)
	}
	p := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := planSplit([]string{"-out", dir, p}); code != 0 {
		t.Errorf("planSplit(over-cap) = %d, want 0", code)
	}
	// At least one sub-plan file written.
	matches, _ := filepath.Glob(filepath.Join(dir, "*part-1-of-*.yaml"))
	if len(matches) == 0 {
		t.Error("planSplit wrote no sub-plan files")
	}
}

func TestCorpusScan_DryRun(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "Foo.java"), []byte("package x;\npublic class Foo {}\n"), 0o644)
	code := corpusScan([]string{
		"-repo", "core-api", "-repo-root", repo, "-module", "m", "-source-root", src, "-dry-run",
	})
	if code != 0 {
		t.Errorf("corpusScan dry-run = %d, want 0", code)
	}
}

func TestCorpusProse_DryRun(t *testing.T) {
	repo := t.TempDir()
	src := filepath.Join(repo, "docs")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "g.md"), []byte("# T\n\nThe tenant id MUST be stamped.\n"), 0o644)
	code := corpusProse([]string{
		"-repo", "core-api", "-repo-root", repo, "-source-root", src, "-dry-run",
	})
	if code != 0 {
		t.Errorf("corpusProse dry-run = %d, want 0", code)
	}
}

func TestCorpusClassify_DryRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prose.yaml")
	_ = os.WriteFile(p, []byte("- id: s1\n  text: a sentence\n"), 0o644)
	if code := corpusClassify([]string{"-input", p, "-dry-run"}); code != 0 {
		t.Errorf("corpusClassify dry-run = %d, want 0", code)
	}
}

func TestDatasetSanitizeDocs(t *testing.T) {
	if code := datasetSanitizeDocs(nil); code != 2 {
		t.Errorf("sanitize-docs(nil) = %d, want 2", code)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("# doc\n\ntext\n"), 0o644)
	if code := datasetSanitizeDocs([]string{"-root", dir, "-dry-run"}); code != 0 {
		t.Errorf("sanitize-docs dry-run = %d, want 0", code)
	}
}

func TestDatasetIndex_DryRunEmptyKB(t *testing.T) {
	oURL, cURL := indexServers(t)
	kb := t.TempDir()
	code := datasetIndex([]string{"-kb-root", kb, "-ollama-url", oURL, "-chroma-url", cURL, "-dry-run"})
	if code != 0 {
		t.Errorf("datasetIndex dry-run (empty kb) = %d, want 0", code)
	}
}

func TestHistoryStats_Manifest(t *testing.T) {
	dir := t.TempDir()
	man := filepath.Join(dir, "history-manifest.yaml")
	_ = os.WriteFile(man, []byte("builder_version: v1\nrepos: []\n"), 0o644)
	// No chroma URL → skips the collection-size probe; manifest EMPTY → exit 0.
	if code := historyStats([]string{"-manifest", man}); code != 0 {
		t.Errorf("historyStats = %d, want 0", code)
	}
}

func TestHistoryIndex_Integration(t *testing.T) {
	oURL, cURL := indexServers(t)
	platform := t.TempDir()
	gitRepoWithCommits(t, mkdir(t, filepath.Join(platform, "core-api")))
	kb := mkdir(t, filepath.Join(platform, "knowledge-base"))
	code := historyIndex([]string{
		"-platform-root", platform, "-kb-root", kb, "-repo", "core-api",
		"-ollama-url", oURL, "-chroma-url", cURL, "-full-scan",
		"-manifest", filepath.Join(platform, "m.yaml"),
	})
	if code != 0 {
		t.Errorf("historyIndex = %d, want 0", code)
	}
}
