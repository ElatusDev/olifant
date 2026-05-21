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

func TestExtractTypeScript_ComponentHookTypeInterfaceEnumConstEndpoint(t *testing.T) {
	src := "import React from 'react';\n" +
		"\n" +
		"export interface AuthUser {\n" +
		"  uid: string;\n" +
		"  email: string;\n" +
		"}\n" +
		"\n" +
		"export type LoginResult = { token: string };\n" +
		"\n" +
		"export enum AuthStatus { Idle, Loading, Succeeded, Failed }\n" +
		"\n" +
		"export const API_LOGIN_PATH = '/v1/security/login';\n" +
		"\n" +
		"const LoginPage: React.FC = () => {\n" +
		"  return <div>login</div>;\n" +
		"};\n" +
		"\n" +
		"export function useAuthGuard(): boolean {\n" +
		"  return true;\n" +
		"}\n" +
		"\n" +
		"export const useGoogleSignIn = () => {\n" +
		"  return null;\n" +
		"};\n" +
		"\n" +
		"export const authApi = baseApi.injectEndpoints({\n" +
		"  endpoints: (builder) => ({\n" +
		"    loginUser: builder.mutation<AuthTokenResponseDTO, LoginRequestDTO>({ query: c => ({ url: '/x', method: 'POST', body: c }) }),\n" +
		"    getCurrentUser: builder.query<AuthUser, void>({ query: () => '/me' }),\n" +
		"  }),\n" +
		"});\n"

	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "akademia-plus-web")
	srcRoot := filepath.Join(repoRoot, "src", "features", "auth")
	tsPath := filepath.Join(srcRoot, "Sample.tsx")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tsPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractTypeScript(tsPath, "src/features/auth/Sample.tsx", ScanConfig{
		Repo:     "akademia-plus-web",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractTypeScript: %v", err)
	}

	wantKinds := map[string]int{
		KindInterface: 1,
		KindType:      1,
		KindEnum:      1,
		KindConstant:  1,
		KindComponent: 1,
		KindHook:      2,
		KindEndpoint:  2,
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

	// Tag sanity on the component symbol.
	var comp *Symbol
	for i := range syms {
		if syms[i].Tags[AxisKind] == KindComponent {
			comp = &syms[i]
			break
		}
	}
	if comp == nil {
		t.Fatal("no component symbol found")
	}
	if comp.Tags[AxisLanguage] != LangTypeScript {
		t.Errorf("language: got %v want %s", comp.Tags[AxisLanguage], LangTypeScript)
	}
	if comp.Tags[AxisScope] != ScopeWebapp {
		t.Errorf("scope: got %v want %s", comp.Tags[AxisScope], ScopeWebapp)
	}
	concerns, _ := comp.Tags[AxisConcern].([]string)
	var gotSec bool
	for _, c := range concerns {
		if c == ConcernSecurity {
			gotSec = true
		}
	}
	if !gotSec {
		t.Errorf("concerns missing security (path contains 'auth'): %v", concerns)
	}
}

func TestIsTestFile(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"src/features/auth/__tests__/X.test.ts", true},
		{"src/features/auth/X.test.tsx", true},
		{"src/features/auth/X.spec.ts", true},
		{"src/features/auth/X.spec.tsx", true},
		{"e2e/features/auth.test.ts", true},
		{"tests/integration/x.ts", true},
		{"src/features/auth/X.ts", false},
		{"src/features/auth/X.tsx", false},
		{"src/features/auth/components/LoginForm.tsx", false},
	}
	for _, tc := range cases {
		if got := isTestFile(tc.p); got != tc.want {
			t.Errorf("isTestFile(%q) = %v want %v", tc.p, got, tc.want)
		}
	}
}

func TestClassifyTSCallable(t *testing.T) {
	cases := []struct {
		n, want string
	}{
		{"useAuth", KindHook},
		{"useState", KindHook},
		{"LoginPage", KindComponent},
		{"AuthProvider", KindComponent},
		{"handleClick", ""}, // camelCase helper — skipped
		{"x", ""},
		{"use", ""},        // too short
		{"useany", ""},     // 'use' + lowercase — not a hook by convention
		{"User", KindComponent},
	}
	for _, tc := range cases {
		if got := classifyTSCallable(tc.n); got != tc.want {
			t.Errorf("classifyTSCallable(%q) = %q want %q", tc.n, got, tc.want)
		}
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
