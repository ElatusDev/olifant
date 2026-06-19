package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// ===== dispatcher usage paths (no-arg + unknown subcommand → exit 2) =====

func TestDispatchers_UsageExitTwo(t *testing.T) {
	dispatch := map[string]func([]string) int{
		"corpus": Corpus, "dataset": Dataset, "eval": Eval, "history": History,
		"plan": Plan, "prompt": Prompt, "repo": Repo, "turn": Turn, "dictionary": Dictionary,
	}
	for name, fn := range dispatch {
		if code := fn(nil); code != 2 {
			t.Errorf("%s(nil) = %d, want 2 (usage)", name, code)
		}
		if code := fn([]string{"bogus-subcommand"}); code != 2 {
			t.Errorf("%s(bogus) = %d, want 2 (unknown)", name, code)
		}
	}
}

// ===== pure helpers =====

func TestLanguageHintForPath(t *testing.T) {
	cases := map[string]string{"Foo.java": "java", "a.ts": "typescript", "a.go": "go", "a.unknown": ""}
	for path, want := range cases {
		if got := languageHintForPath(path); got != want {
			t.Errorf("languageHintForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFmtDur(t *testing.T) {
	if got := fmtDur(1500); got != "2s" && got != "1s" {
		t.Errorf("fmtDur(1500) = %q", got)
	}
	if got := fmtDur(0); got != "0s" {
		t.Errorf("fmtDur(0) = %q", got)
	}
}

func TestParseSources(t *testing.T) {
	all, err := parseSources("all")
	if err != nil || len(all) == 0 {
		t.Errorf("parseSources(all) = (%v,%v)", all, err)
	}
	empty, _ := parseSources("")
	if len(empty) != len(all) {
		t.Errorf("empty should default to all")
	}
	got, err := parseSources("retros,decisions")
	if err != nil || len(got) != 2 {
		t.Errorf("parseSources(csv) = (%v,%v)", got, err)
	}
	if _, err := parseSources("bogus-source"); err == nil {
		t.Error("unknown source should error")
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b"}, "b") {
		t.Error("contains should find b")
	}
	if contains([]string{"a"}, "z") {
		t.Error("contains should not find z")
	}
}

func TestListTurnFiles(t *testing.T) {
	// Missing dir → nil, no error.
	got, err := listTurnFiles(filepath.Join(t.TempDir(), "none"))
	if err != nil || got != nil {
		t.Errorf("missing dir = (%v,%v), want (nil,nil)", got, err)
	}
	// Populated dir → only .yaml, sorted.
	dir := t.TempDir()
	for _, n := range []string{"b.yaml", "a.yaml", "note.txt"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	files, err := listTurnFiles(dir)
	if err != nil {
		t.Fatalf("listTurnFiles: %v", err)
	}
	if len(files) != 2 || files[0] != "a.yaml" || files[1] != "b.yaml" {
		t.Errorf("listTurnFiles = %v, want [a.yaml b.yaml]", files)
	}
}

// ===== plan validate / split (filesystem, no network) =====

func writePlanFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanValidate(t *testing.T) {
	dir := t.TempDir()
	if code := planValidate(nil); code != 2 {
		t.Errorf("planValidate(nil) = %d, want 2", code)
	}
	if code := planValidate([]string{filepath.Join(dir, "missing.yaml")}); code != 1 {
		t.Errorf("planValidate(missing) = %d, want 1", code)
	}

	good := writePlanFile(t, dir, "good.yaml", "plan_id: p1\ngoal: g\nsteps:\n  - id: step_01\n    description: d\n")
	if code := planValidate([]string{good}); code != 0 {
		t.Errorf("planValidate(good) = %d, want 0", code)
	}

	bad := writePlanFile(t, dir, "bad.yaml", "plan_id: p2\ngoal: g\nsteps:\n  - id: step_01\n    description: d\n  - id: step_01\n    description: dup\n")
	if code := planValidate([]string{bad}); code != 1 {
		t.Errorf("planValidate(dup-ids) = %d, want 1", code)
	}
}

func TestPlanSplit_UnderCap(t *testing.T) {
	dir := t.TempDir()
	p := writePlanFile(t, dir, "small.yaml", "plan_id: p1\ngoal: g\nsteps:\n  - id: step_01\n    description: d\n")
	if code := planSplit([]string{p}); code != 0 {
		t.Errorf("planSplit(under-cap) = %d, want 0", code)
	}
	if code := planSplit(nil); code != 2 {
		t.Errorf("planSplit(no-path) = %d, want 2", code)
	}
}

// ===== corpus build / dataset build / dict bootstrap / history scan (dry/hermetic) =====

func TestCorpusBuild_Hermetic(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(filepath.Join(kb, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(kb, "patterns", "backend.md"), []byte("# P\n\n## Caching\n\nbody\n"), 0o644)
	mem := filepath.Join(root, "mem")
	_ = os.MkdirAll(mem, 0o755)

	code := corpusBuild([]string{
		"-kb-root", kb,
		"-platform-root", root,
		"-memory-root", mem,
		"-out", filepath.Join(root, "out"),
	})
	if code != 0 {
		t.Errorf("corpusBuild = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "manifest.yaml")); err != nil {
		t.Errorf("corpus manifest not written: %v", err)
	}
}

func TestDatasetBuild_EmptyKBFailsGracefully(t *testing.T) {
	// An empty KB has no extractable sources → graceful exit 1, no crash.
	kb := t.TempDir()
	code := datasetBuild([]string{"-kb-root", kb, "-dry-run", "-sources", "decisions"})
	if code != 1 {
		t.Errorf("datasetBuild over empty KB = %d, want 1 (graceful failure)", code)
	}
}

func TestDatasetBuild_BadSourceExitsTwo(t *testing.T) {
	code := datasetBuild([]string{"-kb-root", t.TempDir(), "-sources", "bogus"})
	if code != 2 {
		t.Errorf("datasetBuild bad source = %d, want 2", code)
	}
}

func TestDictBootstrap_DryRun(t *testing.T) {
	root := t.TempDir()
	corpusDir := filepath.Join(root, "corpus")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(corpusDir, "universal.ndjson"),
		[]byte(`{"artifact_id":"D154","title":"x","scope":"universal"}`+"\n"), 0o644)

	code := dictBootstrap([]string{
		"-kb-root", root,
		"-corpus-dir", corpusDir,
		"-dictionary-dir", filepath.Join(root, "dict"),
		"-dry-run",
	})
	if code != 0 {
		t.Errorf("dictBootstrap dry-run = %d, want 0", code)
	}
}

func TestHistoryScan_NoReposIsClean(t *testing.T) {
	root := t.TempDir() // platform root with no repo subdirs → all skipped
	kb := filepath.Join(root, "knowledge-base")
	_ = os.MkdirAll(kb, 0o755)
	code := historyScan([]string{
		"-platform-root", root,
		"-kb-root", kb,
		"-out", filepath.Join(root, "out"),
		"-dry-run",
	})
	if code != 0 {
		t.Errorf("historyScan (no repos) = %d, want 0", code)
	}
}
