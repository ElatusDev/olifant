//go:build integration

package repos_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/repos"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	_ = os.WriteFile(filepath.Join(dir, "Foo.java"),
		[]byte("package x;\npublic class Foo {\n  Long tenantId;\n}\n"), 0o644)
	run("add", "-A")
	return dir
}

// TestLive_IngestRepo chunks + embeds a tiny git repo into an isolated
// code_itest collection on the live stack and counts it back.
func TestLive_IngestRepo(t *testing.T) {
	rt := livetest.RequireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := gitRepo(t)
	stats, err := repos.Ingest(ctx, repos.IngestConfig{
		Repos:     []repos.RepoSpec{{Name: "itest", Path: repo, Scope: "itest"}},
		OllamaURL: rt.OllamaURL,
		ChromaURL: rt.ChromaURL,
		Embedder:  rt.Embedder,
		Tenant:    rt.ChromaTenant,
		Database:  rt.ChromaDatabase,
	})
	if err != nil {
		t.Fatalf("repos.Ingest: %v", err)
	}
	if stats.ChunksUpserted < 1 {
		t.Errorf("ChunksUpserted = %d, want >= 1", stats.ChunksUpserted)
	}

	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
	coll, err := cc.EnsureCollection(ctx, "code_itest", nil)
	if err != nil {
		t.Fatalf("EnsureCollection code_itest: %v", err)
	}
	n, err := cc.Count(ctx, coll.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n < 1 {
		t.Errorf("code_itest count = %d, want >= 1", n)
	}
	t.Logf("ingested files=%d chunks=%d code_itest count=%d",
		stats.FilesRead, stats.ChunksUpserted, n)
}
