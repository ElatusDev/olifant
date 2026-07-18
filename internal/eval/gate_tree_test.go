package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ElatusDev/olifant/internal/kbtree"
)

// fixtureKBRepo creates a temp git repo shaped like the KB's gate-read
// surface (a suite + the two manifests), commits it, and returns the dir.
func fixtureKBRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"eval/suites/mini-v1.yaml":     "suite_id: mini-v1\ncases:\n  - id: c1\n    scope: [universal]\n    request: \"hello\"\n",
		"corpus/v1/manifest.yaml":      "built_at: x\nsources: 3\n",
		"corpus/v1/repo-manifest.yaml": "repos: 7\n",
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

func TestTreeSHA256_DualModeDigestEquivalence(t *testing.T) {
	repo := fixtureKBRepo(t)
	gt, err := kbtree.Git(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	fs := kbtree.FS(repo)
	for _, rel := range []string{
		"eval/suites/mini-v1.yaml",
		"corpus/v1/manifest.yaml",
		"corpus/v1/repo-manifest.yaml",
	} {
		fileSHA, err := FileSHA256(filepath.Join(repo, rel))
		if err != nil {
			t.Fatal(err)
		}
		fsSHA, err := TreeSHA256(fs, rel)
		if err != nil {
			t.Fatal(err)
		}
		gitSHA, err := TreeSHA256(gt, rel)
		if err != nil {
			t.Fatal(err)
		}
		if fileSHA == "" || fileSHA != fsSHA || fsSHA != gitSHA {
			t.Errorf("%s: digests diverge file=%s fs=%s git=%s", rel, fileSHA, fsSHA, gitSHA)
		}
	}
}

func TestTreeSHA256_MissingFileDegrade(t *testing.T) {
	repo := fixtureKBRepo(t)
	gt, err := kbtree.Git(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, kb := range []kbtree.Tree{kbtree.FS(repo), gt} {
		sha, err := TreeSHA256(kb, "eval/suites/absent.yaml")
		if sha != "" || err != nil {
			t.Errorf("missing file must degrade to (\"\", nil), got (%q, %v)", sha, err)
		}
	}
}

func TestTreeSHA256_RefBlobsNotWorkingTree(t *testing.T) {
	// The no-fallback proof: dirty the working tree after commit; the git
	// tree must keep hashing the ref's blob, not the modified file.
	repo := fixtureKBRepo(t)
	rel := "eval/suites/mini-v1.yaml"
	committed, err := FileSHA256(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, rel), []byte("suite_id: tampered\ncases:\n  - id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt, err := kbtree.Git(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gitSHA, err := TreeSHA256(gt, rel)
	if err != nil {
		t.Fatal(err)
	}
	dirtySHA, err := TreeSHA256(kbtree.FS(repo), rel)
	if err != nil {
		t.Fatal(err)
	}
	if gitSHA != committed {
		t.Errorf("git mode must hash the ref blob: got %s want %s", gitSHA, committed)
	}
	if gitSHA == dirtySHA {
		t.Error("git and dirty-fs digests should differ — fallback suspected")
	}
}

func TestLoadSuiteBytes_EquivalentToLoadSuite(t *testing.T) {
	repo := fixtureKBRepo(t)
	rel := "eval/suites/mini-v1.yaml"
	fromFile, err := LoadSuite(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	gt, err := kbtree.Git(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := gt.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	fromBlob, err := LoadSuiteBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.SuiteID != fromBlob.SuiteID || len(fromFile.Cases) != len(fromBlob.Cases) {
		t.Errorf("suite parse diverges: file=%+v blob=%+v", fromFile, fromBlob)
	}
	if _, err := LoadSuiteBytes([]byte("cases: []\n")); err == nil {
		t.Error("LoadSuiteBytes must keep LoadSuite's validation (suite_id required)")
	}
}
