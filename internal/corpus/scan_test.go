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

func TestExtractHCL_ResourceDataModuleVariableOutputProvider(t *testing.T) {
	src := `# Sample terraform file

provider "aws" {
  region = var.region
}

variable "region" {
  type    = string
  default = "us-east-1"
}

data "aws_caller_identity" "current" {}

resource "aws_s3_bucket" "app_storage" {
  bucket = "akademiaplus-app"
}

module "networking" {
  source = "./modules/networking"
}

output "vpc_id" {
  value = module.networking.vpc_id
}
`
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "infra")
	srcRoot := filepath.Join(repoRoot, "terraform")
	tfPath := filepath.Join(srcRoot, "sample.tf")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tfPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractHCL(tfPath, "terraform/sample.tf", ScanConfig{
		Repo:     "infra",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractHCL: %v", err)
	}

	wantKinds := map[string]int{
		KindResource:  2, // resource + data
		KindModule:    1,
		KindVariable:  1,
		KindOutput:    1,
		KindConfigKey: 1, // provider
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
	// Resource text should be "type.name"
	for _, s := range syms {
		if s.Tags[AxisKind] == KindResource && !strings.Contains(s.Text, ".") {
			t.Errorf("resource text missing type.name: %q", s.Text)
		}
		if s.Tags[AxisLanguage] != LangHCL {
			t.Errorf("language: got %v want %s for symbol %q", s.Tags[AxisLanguage], LangHCL, s.Text)
		}
	}
}

func TestExtractPostman_CollectionFoldersRequestsVariables(t *testing.T) {
	src := `{
  "info": {"name": "task-service-e2e", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
  "item": [
    {"name": "Setup", "item": [
      {"name": "LoginInternalForTokens", "request": {"method": "POST"}}
    ]},
    {"name": "CRUD", "item": [
      {"name": "CreateTask", "request": {"method": "POST"}},
      {"name": "GetTask", "request": {"method": "GET"}}
    ]},
    {"name": "TopLevelRequest", "request": {"method": "GET"}}
  ],
  "variable": [{"key": "authToken", "value": ""}]
}`
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "core-api-e2e")
	jsonPath := filepath.Join(repoRoot, "Postman Collections", "task-service-e2e.postman_collection.json")
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractPostman(jsonPath, "Postman Collections/task-service-e2e.postman_collection.json", ScanConfig{
		Repo:     "core-api-e2e",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractPostman: %v", err)
	}

	wantKinds := map[string]int{
		KindResource:  3, // collection + 2 folders
		KindEndpoint:  4, // 3 leaf requests + 1 top-level
		KindConfigKey: 1, // variable
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
	// Verify ScopeE2E + language=json on at least one symbol.
	if len(syms) == 0 {
		t.Fatal("no symbols emitted")
	}
	if syms[0].Tags[AxisLanguage] != LangJSON {
		t.Errorf("language: got %v want %s", syms[0].Tags[AxisLanguage], LangJSON)
	}
	if syms[0].Tags[AxisScope] != ScopeE2E {
		t.Errorf("scope: got %v want %s", syms[0].Tags[AxisScope], ScopeE2E)
	}
}

func TestExtractKBMarkdown_IDHeadersAndConceptHeaders(t *testing.T) {
	src := `# Decision Log

> Chronological log.

## D1: MongoDB as ETL Staging — 2026-03-09

Body.

## AP3: Polling Without Max Retry

Body.

## Domain Object Pattern

Body — no ID prefix, captured as concept.

### The Problem

(H3 — still captured, no ID)
`
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "knowledge-base")
	mdPath := filepath.Join(repoRoot, "decisions", "log.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractKB(mdPath, "decisions/log.md", ScanConfig{
		Repo:     "knowledge-base",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractKB markdown: %v", err)
	}

	idTexts := map[string]string{}
	conceptTexts := map[string]bool{}
	for _, s := range syms {
		k, _ := s.Tags[AxisKind].(string)
		switch k {
		case KindID:
			fam, _ := s.Tags[AxisIDFamily].(string)
			idTexts[s.Text] = fam
		case KindClass:
			conceptTexts[s.Text] = true
		}
	}
	if got := idTexts["D1"]; got != IDFamilyDecision {
		t.Errorf("D1: got id_family=%q want %q", got, IDFamilyDecision)
	}
	if got := idTexts["AP3"]; got != IDFamilyAntiPattern {
		t.Errorf("AP3: got id_family=%q want %q", got, IDFamilyAntiPattern)
	}
	if !conceptTexts["Domain Object Pattern"] {
		t.Errorf("missing concept header 'Domain Object Pattern' in %+v", conceptTexts)
	}
}

func TestExtractKBYAML_DictionaryTerms(t *testing.T) {
	src := `- term: ABB-01
  category: domain.anti_pattern.backend
  definition: Long Method
  cites:
    - standards/ANTI-PATTERNS-BACKEND.yaml#ABB-01
- term: ABB-02
  category: domain.anti_pattern.backend
  definition: God Class
`
	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "knowledge-base")
	yamlPath := filepath.Join(repoRoot, "dictionary", "backend", "domain.yaml")
	if err := os.MkdirAll(filepath.Dir(yamlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, err := extractKB(yamlPath, "dictionary/backend/domain.yaml", ScanConfig{
		Repo:     "knowledge-base",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractKB yaml: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("expected 2 term symbols, got %d (%+v)", len(syms), syms)
	}
	for _, s := range syms {
		if s.Tags[AxisKind] != KindTerm {
			t.Errorf("expected kind=term, got %v for %q", s.Tags[AxisKind], s.Text)
		}
		if s.Tags[AxisLanguage] != LangYAML {
			t.Errorf("expected language=yaml, got %v", s.Tags[AxisLanguage])
		}
	}
}

func TestIsKBNonCurated(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"decisions/log.md", false},
		{"anti-patterns/catalog.md", false},
		{"patterns/backend.md", false},
		{"dictionary/backend/domain.yaml", false},
		{"retrospectives/some-retro.md", true},
		{"architecture/some-doc.md", true},
		{"README.md", true},
	}
	for _, tc := range cases {
		if got := isKBNonCurated(tc.p); got != tc.want {
			t.Errorf("isKBNonCurated(%q) = %v want %v", tc.p, got, tc.want)
		}
	}
}

func TestIsPostmanBackup(t *testing.T) {
	if !isPostmanBackup("foo.json.bak") {
		t.Error("expected true for foo.json.bak")
	}
	if isPostmanBackup("foo.json") {
		t.Error("expected false for foo.json")
	}
}

func TestExtractProse_TokenizationAndRules(t *testing.T) {
	src := "# Sample Doc\n" +
		"\n" +
		"Tests are NOT optional. Write unit tests with each phase.\n" +
		"\n" +
		"You MUST never push without running the full build first. " +
		"Run the test suite via npm test.\n" +
		"\n" +
		"This sentence is a regular affirmation that should be tagged as such.\n" +
		"\n" +
		"```bash\n" +
		"echo 'this code block should be skipped entirely'\n" +
		"```\n" +
		"\n" +
		"> A blockquote should also be skipped.\n" +
		"\n" +
		"If the build fails, then halt immediately.\n"

	dir := t.TempDir()
	repoRoot := filepath.Join(dir, "knowledge-base")
	mdPath := filepath.Join(repoRoot, "patterns", "sample.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	sents, err := extractProse(mdPath, "patterns/sample.md", ScanConfig{
		Repo:     "knowledge-base",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("extractProse: %v", err)
	}
	if len(sents) < 5 {
		t.Fatalf("expected ≥5 sentences, got %d (%+v)", len(sents), sents)
	}

	// No sentence should reference the code-block content.
	for _, s := range sents {
		if strings.Contains(s.Text, "echo") {
			t.Errorf("code block leaked into sentence: %q", s.Text)
		}
		if strings.Contains(s.Text, "blockquote") {
			t.Errorf("blockquote leaked into sentence: %q", s.Text)
		}
	}

	// At least one MUST → mandatory or forbidden modality must fire.
	gotModal := map[string]bool{}
	for _, s := range sents {
		if m, ok := s.Tags[AxisModality].(string); ok {
			gotModal[m] = true
		}
	}
	if !gotModal[ModalForbidden] && !gotModal[ModalMandatory] {
		t.Errorf("expected at least one mandatory/forbidden modality from MUST/MUST NOT cues, got %+v", gotModal)
	}

	// Conditional sentence should be tagged.
	gotConditional := false
	for _, s := range sents {
		if s.Tags[AxisSyntactic] == SyntConditional {
			gotConditional = true
		}
	}
	if !gotConditional {
		t.Errorf("expected at least one conditional from 'If ... then' sentence")
	}

	// Scope and language are constants from path/repo.
	for _, s := range sents {
		if s.Tags[AxisLanguage] != LangMarkdown {
			t.Errorf("language: got %v want %s", s.Tags[AxisLanguage], LangMarkdown)
		}
		if s.Tags[AxisScope] != ScopePlatformProcess {
			t.Errorf("scope: got %v want %s", s.Tags[AxisScope], ScopePlatformProcess)
		}
	}
}

func TestClassifySyntactic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"This is a statement.", SyntAffirmation},
		{"Is this a question?", SyntQuestion},
		{"Run the build now!", SyntImperative},
		{"Run the build now.", SyntImperative},
		{"Use the X tool.", SyntImperative},
		{"If x then y.", SyntConditional},
		{"do not delete the file.", SyntNegation},
		{"This is not allowed.", SyntNegation},
	}
	for _, tc := range cases {
		got := classifySyntactic(tc.in)
		if got != tc.want {
			t.Errorf("classifySyntactic(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifyModality(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Tests MUST be written before code.", ModalMandatory},
		{"This MUST NOT be skipped.", ModalForbidden},
		{"Code should be reviewed before merge.", ModalRecommended},
		{"You may add comments where helpful.", ModalAllowed},
		{"HARD RULE: never push without testing.", ModalForbidden},
		{"Just a regular sentence.", ""},
	}
	for _, tc := range cases {
		got := classifyModality(tc.in)
		if got != tc.want {
			t.Errorf("classifyModality(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsProseCandidate(t *testing.T) {
	cases := []struct {
		repo, path string
		want       bool
	}{
		{"core-api", "any/path/README.md", true},
		{"knowledge-base", "patterns/backend.md", true},
		{"knowledge-base", "anti-patterns/catalog.md", true},
		{"knowledge-base", "decisions/log.md", true},
		{"knowledge-base", "standards/SECURITY.md", true},
		{"knowledge-base", "architecture/DESIGN.md", true},
		{"knowledge-base", "skills/SKILL.md", true},
		{"knowledge-base", "templates/foo.md", true},
		{"knowledge-base", "dictionary/backend/domain.md", true},
		{"knowledge-base", "runbooks/x.md", true},
		{"knowledge-base", "retrospectives/foo.md", false},
		{"knowledge-base", "operations/x.md", false},
		{"knowledge-base", "audit-report/y.md", false},
	}
	for _, tc := range cases {
		if got := isProseCandidate(tc.repo, tc.path); got != tc.want {
			t.Errorf("isProseCandidate(%q, %q) = %v want %v", tc.repo, tc.path, got, tc.want)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("First sentence. Second one! Third? End.")
	if len(got) != 4 {
		t.Errorf("expected 4 sentences, got %d (%+v)", len(got), got)
	}
}

func TestIsMeaningfulSentence(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"This is a meaningful sentence.", true},
		{"too short", false},
		{"NoSpacesOnlyOneWord", false},
		{"!!!@@@###$$$%%%", false},
		{"A real sentence with several words.", true},
	}
	for _, tc := range cases {
		if got := isMeaningfulSentence(tc.in); got != tc.want {
			t.Errorf("isMeaningfulSentence(%q) = %v want %v", tc.in, got, tc.want)
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
