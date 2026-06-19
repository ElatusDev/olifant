package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfig_ExplicitKBRoot(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, "knowledge-base")
	if err := os.MkdirAll(kb, 0o755); err != nil {
		t.Fatal(err)
	}
	// A platform-level memory dir so the MemoryRoot fallback branch fires.
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := ResolveConfig(Config{KBRoot: kb})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if !filepath.IsAbs(out.KBRoot) {
		t.Errorf("KBRoot not absolute: %q", out.KBRoot)
	}
	if out.PlatformRoot != filepath.Dir(out.KBRoot) {
		t.Errorf("PlatformRoot = %q, want parent of KBRoot", out.PlatformRoot)
	}
	if out.OutDir != filepath.Join(out.KBRoot, "corpus", "v1") {
		t.Errorf("OutDir = %q", out.OutDir)
	}
	// MemoryRoot resolves to either the $HOME/.claude projects memory dir or
	// the platform-level memory dir, whichever exists first — both are valid.
	if out.MemoryRoot == "" {
		t.Error("MemoryRoot not resolved")
	}
}

func TestIDFamilyForPrefix(t *testing.T) {
	cases := map[string]string{
		"D":   IDFamilyDecision,
		"AP":  IDFamilyAntiPattern,
		"PC":  IDFamilyPattern,
		"FM":  IDFamilyFailureMode,
		"WA":  IDFamilyWebappArch,
		"ABS": IDFamilyBackendAP,
		"AWC": IDFamilyWebappAP,
		"AMS": IDFamilyMobileAP,
	}
	for prefix, want := range cases {
		if got := idFamilyForPrefix(prefix); got != want {
			t.Errorf("idFamilyForPrefix(%q) = %q, want %q", prefix, got, want)
		}
	}
	if got := idFamilyForPrefix("ZZ"); got != "" {
		t.Errorf("unknown prefix = %q, want empty", got)
	}
}

func TestRepoProfile(t *testing.T) {
	for _, repo := range []string{"core-api", "elatusdev-web", "infra", "core-api-e2e", "knowledge-base"} {
		if _, ok := repoProfile(repo); !ok {
			t.Errorf("repoProfile(%q) not found", repo)
		}
	}
	if _, ok := repoProfile("unknown-repo"); ok {
		t.Error("unknown repo should return ok=false")
	}
}

func TestScan_Dispatch(t *testing.T) {
	if _, err := Scan(ScanConfig{}); err == nil {
		t.Error("missing RepoRoot should error")
	}
	if _, err := Scan(ScanConfig{RepoRoot: "/r", SourceRoot: "/r/s", Repo: "no-such-repo", DryRun: true}); err == nil {
		t.Error("unknown repo profile should error")
	}

	// core-api .java dispatch through extractJava.
	repo := t.TempDir()
	src := filepath.Join(repo, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Foo.java"),
		[]byte("package x;\npublic class Foo {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ScanConfig{Repo: "core-api", RepoRoot: repo, SourceRoot: src, DryRun: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", stats.FilesScanned)
	}
}
