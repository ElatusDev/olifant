package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractJava_PackageClassInterfaceEnumAnnotation(t *testing.T) {
	src := `package com.akademiaplus.multitenant.repository;

import jakarta.persistence.EntityManager;

public sealed class TenantSoftDeleteGuardFilter
    permits CoreFilter {
    private final EntityManager em;
}

interface SoftDeletable {
    Long deletedAt();
}

@interface IgnoreTenantFilter {
}

enum SoftDeleteMode {
    BLOCK, ALLOW;
}
`
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "core-api")
	srcRoot := filepath.Join(repoRoot, "multi-tenant-data", "src", "main", "java")
	javaPath := filepath.Join(srcRoot, "Sample.java")
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(javaPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractJava(javaPath, "multi-tenant-data/src/main/java/Sample.java", ScanConfig{
		Repo:     "core-api",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractJava: %v", err)
	}

	wantKinds := map[string]int{
		KindPackage:    1,
		KindClass:      1,
		KindInterface:  1,
		KindAnnotation: 1,
		KindEnum:       1,
	}
	got := map[string]int{}
	for _, s := range syms {
		k, _ := s.Tags[AxisKind].(string)
		got[k]++
	}
	for k, n := range wantKinds {
		if got[k] != n {
			t.Errorf("kind=%s: got %d want %d (all: %+v)", k, got[k], n, got)
		}
	}

	// Tag sanity on the package symbol
	var pkg *Symbol
	for i := range syms {
		if syms[i].Tags[AxisKind] == KindPackage {
			pkg = &syms[i]
			break
		}
	}
	if pkg == nil {
		t.Fatal("no package symbol found")
	}
	if pkg.Text != "com.akademiaplus.multitenant.repository" {
		t.Errorf("package text: got %q want com.akademiaplus.multitenant.repository", pkg.Text)
	}
	if pkg.Tags[AxisLanguage] != LangJava {
		t.Errorf("language: got %v want %s", pkg.Tags[AxisLanguage], LangJava)
	}
	if pkg.Tags[AxisScope] != ScopeBackend {
		t.Errorf("scope: got %v want %s", pkg.Tags[AxisScope], ScopeBackend)
	}
	// Path contains "multi-tenant" → concern tenancy expected
	concerns, _ := pkg.Tags[AxisConcern].([]string)
	gotTenancy := false
	for _, c := range concerns {
		if c == ConcernTenancy {
			gotTenancy = true
		}
	}
	if !gotTenancy {
		t.Errorf("concerns missing tenancy: %v", concerns)
	}
}

func TestExtractJava_ContainsCI(t *testing.T) {
	cases := []struct {
		s, needle string
		want      bool
	}{
		{"core-api/multi-tenant-data/src", "MULTI-TENANT", true},
		{"core-api/MultiTenant/src", "multi-tenant", false}, // hyphenated needle, no hyphen in string
		{"core-api/multitenant/", "multitenant", true},
		{"core-api/security/AuthFilter.java", "security", true},
		{"", "anything", false},
	}
	for _, tc := range cases {
		got := containsCI(tc.s, tc.needle)
		if got != tc.want {
			t.Errorf("containsCI(%q, %q) = %v want %v", tc.s, tc.needle, got, tc.want)
		}
	}
}

func TestScanRequiredArgs(t *testing.T) {
	if _, err := Scan(ScanConfig{}); err == nil {
		t.Error("expected error for missing RepoRoot")
	}
	if _, err := Scan(ScanConfig{RepoRoot: "/x"}); err == nil {
		t.Error("expected error for missing SourceRoot")
	}
	if _, err := Scan(ScanConfig{RepoRoot: "/x", SourceRoot: "/x"}); err == nil {
		t.Error("expected error for missing OutPath when not DryRun")
	}
}

func TestScanEndToEnd(t *testing.T) {
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "core-api")
	srcRoot := filepath.Join(repoRoot, "multi-tenant-data", "src", "main", "java")
	javaPath := filepath.Join(srcRoot, "Sample.java")
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(javaPath, []byte("package com.akademiaplus;\nclass Foo {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.yaml")
	stats, err := Scan(ScanConfig{
		Repo:       "core-api",
		RepoRoot:   repoRoot,
		Module:     "multi-tenant-data",
		SourceRoot: srcRoot,
		OutPath:    outPath,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if stats.FilesScanned != 1 || stats.SymbolsEmitted != 2 {
		t.Errorf("stats: %+v", stats)
	}
	body, _ := os.ReadFile(outPath)
	if !strings.Contains(string(body), "com.akademiaplus") {
		t.Errorf("yaml missing package symbol: %s", body)
	}
	if !strings.Contains(string(body), "Foo") {
		t.Errorf("yaml missing class symbol: %s", body)
	}
}
