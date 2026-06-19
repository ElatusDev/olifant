package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusScan_ModuleRequiredPerRepo(t *testing.T) {
	dir := t.TempDir() // valid repo-root; NO --source-root so the module check fires
	for _, repo := range []string{"core-api", "elatusdev-web", "akademia-plus-go", "infra", "core-api-e2e"} {
		code := corpusScan([]string{"-repo", repo, "-repo-root", dir})
		if code != 2 {
			t.Errorf("corpusScan(%s, no module) = %d, want 2", repo, code)
		}
	}
}

func TestCorpusProse_KBModuleRequired(t *testing.T) {
	dir := t.TempDir()
	code := corpusProse([]string{"-repo", "knowledge-base", "-repo-root", dir})
	if code != 2 {
		t.Errorf("corpusProse(knowledge-base, no module) = %d, want 2", code)
	}
}

func TestTurnList_LimitTruncation(t *testing.T) {
	turnTreeChdirN(t, 3) // 3 turn files
	// -n 1 exercises the truncation branch (start = len-1).
	if code := turnList([]string{"-n", "1"}); code != 0 {
		t.Errorf("turnList(-n 1) = %d, want 0", code)
	}
	// -n 0 = show all.
	if code := turnList([]string{"-n", "0"}); code != 0 {
		t.Errorf("turnList(-n 0) = %d, want 0", code)
	}
}

func TestDatasetIndex_NoKBIsError(t *testing.T) {
	t.Chdir(t.TempDir()) // outside platform tree → autodetect fails
	if code := datasetIndex([]string{"-dry-run"}); code != 1 {
		t.Errorf("datasetIndex(no kb) = %d, want 1", code)
	}
}

func TestHistoryStats_NoManifestResolved(t *testing.T) {
	t.Chdir(t.TempDir()) // no kb-root, no -manifest → "(no manifest path resolved)" branch
	if code := historyStats(nil); code != 0 {
		t.Errorf("historyStats(no manifest) = %d, want 0", code)
	}
}

// turnTreeChdirN builds a KB tree with n turn files and chdirs in.
func turnTreeChdirN(t *testing.T, n int) {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	turns := filepath.Join(kb, "short-term", "turns")
	if err := os.MkdirAll(turns, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "README.md"), []byte("# KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := "2026-06-19T08-05-0" + string(rune('0'+i)) + "Z-abc"
		if err := os.WriteFile(filepath.Join(turns, id+".yaml"),
			[]byte("turn_id: "+id+"\nsubcommand: challenge\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(kb, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
}
