//go:build integration

package history_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/history"
	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/livetest"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE=2026-03-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-03-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	_ = os.WriteFile(filepath.Join(dir, "Tenant.java"), []byte("class Tenant {}\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "initial: add Tenant")
	_ = os.WriteFile(filepath.Join(dir, "Tenant.java"), []byte("class Tenant { Long tenantId; }\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "feat: add tenantId field for scoping")
	return dir
}

// TestLive_IndexCommits walks a temp git repo and indexes its commit summaries
// + file snapshots into isolated history_itest / code_history_itest collections
// on the live stack.
func TestLive_IndexCommits(t *testing.T) {
	rt := livetest.RequireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := gitRepo(t)
	records, _, err := history.Walk(ctx, repo, "itest", "itest", "", history.ScanConfig{})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no commit records walked")
	}

	stats, err := history.Index(ctx, records, history.IndexConfig{
		OllamaURL:    rt.OllamaURL,
		ChromaURL:    rt.ChromaURL,
		ChromaTenant: rt.ChromaTenant,
		ChromaDB:     rt.ChromaDatabase,
		Embedder:     rt.Embedder,
	})
	if err != nil {
		t.Fatalf("history.Index: %v", err)
	}
	if stats.CommitUpserted+stats.SnapshotUpserted == 0 {
		t.Errorf("nothing upserted: %+v", stats)
	}

	cc := chroma.New(rt.ChromaURL, rt.ChromaTenant, rt.ChromaDatabase)
	coll, err := cc.EnsureCollection(ctx, "history_itest", nil)
	if err != nil {
		t.Fatalf("EnsureCollection history_itest: %v", err)
	}
	n, err := cc.Count(ctx, coll.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n < 1 {
		t.Errorf("history_itest count = %d, want >= 1", n)
	}
	t.Logf("commits=%d snapshots=%d history_itest count=%d",
		stats.CommitUpserted, stats.SnapshotUpserted, n)
}
