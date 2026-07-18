package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ElatusDev/olifant/internal/eval"
	"github.com/ElatusDev/olifant/internal/kbtree"
)

// gitrefFixtureKB creates a temp git repo shaped like the gate's KB-read
// surface: the default suite set's required suite + both manifests.
func gitrefFixtureKB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"eval/suites/code-feeding-v2.yaml": "suite_id: code-feeding-v2\ncases:\n  - id: c1\n    scope: [universal]\n    request: \"hello\"\n",
		"corpus/v1/manifest.yaml":          "built_at: x\nsources: 3\n",
		"corpus/v1/repo-manifest.yaml":     "repos: 7\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestEvalGateCheck_GitRefStaleWithoutReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	kb := gitrefFixtureKB(t)
	if code := evalGateCheck([]string{"-kb-root", kb, "-git-ref", "HEAD"}); code != gateExitFail {
		t.Fatalf("gate-check -git-ref with no receipts = %d; want %d (STALE)", code, gateExitFail)
	}
}

func TestEvalGateCheck_GitRefFreshWithMatchingReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	kb := gitrefFixtureKB(t)
	gt, err := kbtree.Git(kb, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	suiteSHA, _ := eval.TreeSHA256(gt, "eval/suites/code-feeding-v2.yaml")
	corpusSHA, _ := eval.TreeSHA256(gt, "corpus/v1/manifest.yaml")
	repoSHA, _ := eval.TreeSHA256(gt, "corpus/v1/repo-manifest.yaml")
	rec := eval.Receipt{
		Verdict: "PASS", SuiteID: "code-feeding-v2", GitSHA: headSHA(),
		SuiteSHA: suiteSHA, CorpusSHA: corpusSHA, RepoSHA: repoSHA,
		RunID: "test-run", CleanCases: 1, TotalCases: 1, Timestamp: "2026-07-18T00:00:00Z",
	}
	if err := eval.WriteReceipt("", receiptsLogPath(), rec); err != nil {
		t.Fatal(err)
	}
	// The optional real-usage suite is absent in the fixture (named SKIP);
	// the required suite's ref-blob fingerprints must match the receipt.
	if code := evalGateCheck([]string{"-kb-root", kb, "-git-ref", "HEAD"}); code != gateExitPass {
		t.Fatalf("gate-check -git-ref with matching receipt = %d; want %d (FRESH)", code, gateExitPass)
	}
}

func TestEvalGateCheck_GitRefDirtyWorktreeStillFresh(t *testing.T) {
	// The no-fallback proof at the cmd layer: tamper the working tree after
	// minting the receipt from the ref's blobs — git mode must stay FRESH
	// (reads the ref), while fs mode goes STALE (reads the dirty file).
	t.Setenv("HOME", t.TempDir())
	kb := gitrefFixtureKB(t)
	gt, err := kbtree.Git(kb, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	suiteSHA, _ := eval.TreeSHA256(gt, "eval/suites/code-feeding-v2.yaml")
	corpusSHA, _ := eval.TreeSHA256(gt, "corpus/v1/manifest.yaml")
	repoSHA, _ := eval.TreeSHA256(gt, "corpus/v1/repo-manifest.yaml")
	rec := eval.Receipt{
		Verdict: "PASS", SuiteID: "code-feeding-v2", GitSHA: headSHA(),
		SuiteSHA: suiteSHA, CorpusSHA: corpusSHA, RepoSHA: repoSHA,
		RunID: "test-run", CleanCases: 1, TotalCases: 1, Timestamp: "2026-07-18T00:00:00Z",
	}
	if err := eval.WriteReceipt("", receiptsLogPath(), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "corpus", "v1", "manifest.yaml"), []byte("tampered: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := evalGateCheck([]string{"-kb-root", kb, "-git-ref", "HEAD"}); code != gateExitPass {
		t.Fatalf("git mode should read ref blobs (FRESH), got %d", code)
	}
	if code := evalGateCheck([]string{"-kb-root", kb}); code != gateExitFail {
		t.Fatalf("fs mode should see the dirty manifest (STALE), got %d", code)
	}
}

func TestEvalGate_BadRefIsHardError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	kb := gitrefFixtureKB(t)
	if code := evalGate([]string{"-kb-root", kb, "-git-ref", "no-such-ref"}); code != gateExitUsage {
		t.Fatalf("gate with bad ref = %d; want %d (hard error, no fallback)", code, gateExitUsage)
	}
	if code := evalGate([]string{"-kb-root", kb, "-git-ref", "--output=/tmp/x"}); code != gateExitUsage {
		t.Fatalf("gate with dash-prefixed ref = %d; want %d", code, gateExitUsage)
	}
	if code := evalGateCheck([]string{"-kb-root", kb, "-git-ref", "no-such-ref"}); code != gateExitUsage {
		t.Fatalf("gate-check with bad ref = %d; want %d", code, gateExitUsage)
	}
}
