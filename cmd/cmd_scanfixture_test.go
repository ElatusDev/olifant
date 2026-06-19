package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirP(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// These drive corpusScan's per-repo source-root convention branches (module
// provided, no --source-root) plus the scan+stats path, with the directory
// layout each repo profile expects.

func TestCorpusScan_CoreApiConvention(t *testing.T) {
	rr := t.TempDir()
	src := mkdirP(t, filepath.Join(rr, "billing", "src", "main", "java"))
	_ = os.WriteFile(filepath.Join(src, "Foo.java"), []byte("package x;\npublic class Foo {}\n"), 0o644)
	if code := corpusScan([]string{"-repo", "core-api", "-repo-root", rr, "-module", "billing", "-dry-run", "-v"}); code != 0 {
		t.Errorf("corpusScan core-api convention = %d, want 0", code)
	}
}

func TestCorpusScan_InfraRootConvention(t *testing.T) {
	rr := t.TempDir()
	tf := mkdirP(t, filepath.Join(rr, "terraform"))
	_ = os.WriteFile(filepath.Join(tf, "main.tf"), []byte("resource \"aws_s3_bucket\" \"b\" {}\n"), 0o644)
	if code := corpusScan([]string{"-repo", "infra", "-repo-root", rr, "-module", "root", "-dry-run"}); code != 0 {
		t.Errorf("corpusScan infra root = %d, want 0", code)
	}
}

func TestCorpusScan_InfraModuleConvention(t *testing.T) {
	rr := t.TempDir()
	mod := mkdirP(t, filepath.Join(rr, "terraform", "modules", "vpc"))
	_ = os.WriteFile(filepath.Join(mod, "main.tf"), []byte("variable \"cidr\" {}\n"), 0o644)
	if code := corpusScan([]string{"-repo", "infra", "-repo-root", rr, "-module", "vpc", "-dry-run"}); code != 0 {
		t.Errorf("corpusScan infra module = %d, want 0", code)
	}
}

func TestCorpusScan_WebappConvention(t *testing.T) {
	rr := t.TempDir()
	feat := mkdirP(t, filepath.Join(rr, "src", "features", "billing"))
	_ = os.WriteFile(filepath.Join(feat, "api.ts"), []byte("export const x = 1\n"), 0o644)
	if code := corpusScan([]string{"-repo", "elatusdev-web", "-repo-root", rr, "-module", "billing", "-dry-run"}); code != 0 {
		t.Errorf("corpusScan webapp convention = %d, want 0", code)
	}
}

func TestCorpusScan_SourceRootMissing(t *testing.T) {
	rr := t.TempDir() // core-api module dir does NOT exist → source-root stat fails
	if code := corpusScan([]string{"-repo", "core-api", "-repo-root", rr, "-module", "ghost", "-dry-run"}); code != 1 {
		t.Errorf("corpusScan(missing source-root) = %d, want 1", code)
	}
}

func TestCorpusScan_UnknownRepoDefault(t *testing.T) {
	rr := t.TempDir() // unknown repo → no convention → default branch → exit 1
	if code := corpusScan([]string{"-repo", "mystery", "-repo-root", rr}); code != 1 {
		t.Errorf("corpusScan(unknown repo) = %d, want 1", code)
	}
}
