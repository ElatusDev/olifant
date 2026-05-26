package format

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, y string) *VerdictDoc {
	t.Helper()
	doc, err := ParseVerdictYAML([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestValidate_HappyPaths(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "VALID — confirms with cite",
			yaml: `challenge:
  request: "Use composite key (tenantId, invoiceId) on InvoiceDataModel."
  verdict: VALID
  confirms:
    - claim: "Composite key with tenantId enforces row-level isolation"
      cites: [AP3]
  contradicts: []
  clarify: []
  applicable_rules:
    standards: []
    patterns: [TenantScoped]
    anti_patterns_to_avoid: [AP3]
    decisions_to_honor: []
  proceed: proceed_directly
`,
		},
		{
			name: "VALID_WITH_CAVEATS — confirms + clarify",
			yaml: `challenge:
  request: "Add Domain object for Invoice"
  verdict: VALID_WITH_CAVEATS
  confirms:
    - claim: "Domain object pattern is canonical"
      cites: [PC15]
  contradicts: []
  clarify:
    - question: "Should the domain expose a fluent API?"
      why_asking: "Pattern variants split on this"
  applicable_rules:
    standards: []
    patterns: []
    anti_patterns_to_avoid: []
    decisions_to_honor: []
  proceed: confirm_with_user
`,
		},
		{
			name: "INVALID — contradicts with cite + counter",
			yaml: `challenge:
  request: "Persist Firebase tokens in AsyncStorage"
  verdict: INVALID
  confirms: []
  contradicts:
    - claim: "Storing auth tokens in AsyncStorage"
      counter: "AsyncStorage is not encrypted at rest; use Keychain/Keystore"
      cites: [AMS-02]
  clarify: []
  applicable_rules:
    standards: [AMS-02]
    patterns: []
    anti_patterns_to_avoid: [AMS-02]
    decisions_to_honor: []
  proceed: abort
`,
		},
		{
			name: "NEEDS_CLARIFICATION — populated clarify",
			yaml: `challenge:
  request: "Add a profile edit screen"
  verdict: NEEDS_CLARIFICATION
  confirms: []
  contradicts: []
  clarify:
    - question: "Webapp or mobile?"
      why_asking: "Different stacks (MUI v7 vs RN Paper MD3)"
  applicable_rules:
    standards: []
    patterns: []
    anti_patterns_to_avoid: []
    decisions_to_honor: []
  proceed: confirm_with_user
`,
		},
		{
			name: "OUT_OF_SCOPE — all empty ok",
			yaml: `challenge:
  request: "Best Python lib for web scraping?"
  verdict: OUT_OF_SCOPE
  confirms: []
  contradicts: []
  clarify: []
  applicable_rules:
    standards: []
    patterns: []
    anti_patterns_to_avoid: []
    decisions_to_honor: []
  proceed: abort
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParse(t, tc.yaml)
			if err := doc.Validate(); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
		})
	}
}

func TestValidate_RejectsHARDRULE5Violations(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring expected in the error
	}{
		{
			name: "NEEDS_CLARIFICATION with empty clarify (the dominant A3/A2.5 bug)",
			yaml: `challenge:
  request: "Foo"
  verdict: NEEDS_CLARIFICATION
  confirms: []
  contradicts: []
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: confirm_with_user
`,
			want: "NEEDS_CLARIFICATION requires ≥1 clarify[] entry",
		},
		{
			name: "INVALID with empty contradicts",
			yaml: `challenge:
  request: "Foo"
  verdict: INVALID
  confirms: []
  contradicts: []
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: abort
`,
			want: "INVALID requires ≥1 contradicts[] entry",
		},
		{
			name: "VALID with contradicts",
			yaml: `challenge:
  request: "Foo"
  verdict: VALID
  confirms: []
  contradicts:
    - claim: "X"
      counter: "Y"
      cites: [AP3]
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: proceed_directly
`,
			want: "VALID forbids contradicts[]",
		},
		{
			name: "VALID_WITH_CAVEATS with both confirms and clarify empty",
			yaml: `challenge:
  request: "Foo"
  verdict: VALID_WITH_CAVEATS
  confirms: []
  contradicts: []
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: confirm_with_user
`,
			want: "VALID_WITH_CAVEATS requires ≥1 entry in confirms[] OR clarify[]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParse(t, tc.yaml)
			err := doc.Validate()
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestValidate_RejectsHARDRULE6Violations(t *testing.T) {
	yaml := `challenge:
  request: "Foo"
  verdict: VALID
  confirms:
    - claim: "X"
      cites: []
  contradicts: []
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: proceed_directly
`
	doc := mustParse(t, yaml)
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty cites[], got nil")
	}
	if !strings.Contains(err.Error(), "confirms[0]: requires ≥1 cite") {
		t.Errorf("expected HARD RULE 6 message, got: %v", err)
	}
}

func TestValidate_ProceedMustMatchVerdict(t *testing.T) {
	yaml := `challenge:
  request: "Foo"
  verdict: INVALID
  confirms: []
  contradicts:
    - claim: "X"
      counter: "Y"
      cites: [AP3]
  clarify: []
  applicable_rules: {standards: [], patterns: [], anti_patterns_to_avoid: [], decisions_to_honor: []}
  proceed: proceed_directly
`
	doc := mustParse(t, yaml)
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected error: proceed=proceed_directly doesn't match INVALID")
	}
	if !strings.Contains(err.Error(), "challenge.proceed") {
		t.Errorf("expected proceed mismatch message, got: %v", err)
	}
}

func TestIsAcceptableCite(t *testing.T) {
	cases := []struct {
		cite string
		want bool
	}{
		// Acceptable — artifact IDs
		{"D17", true},
		{"D139", true},
		{"AP3", true},
		{"AP95", true},
		{"PC15", true},
		{"FM4", true},
		{"AMS-02", true},
		{"WA-W03", true},
		{"IMF1", true},
		// Acceptable — fully-qualified paths
		{"core-api/multi-tenant-data/src/main/java/foo.java", true},
		{"core-api/multi-tenant-data/src/main/java/foo.java#L1-L80", true},
		{"knowledge-base/decisions/log.yaml#D17", true},
		{"akademia-plus-web/src/features/tasks/TasksPage.tsx", true},
		// Rejected — bare filenames
		{"README.md", false},
		{"CLAUDE.md", false},
		{"foo.go", false},
		// Rejected — partial / non-anchored paths
		{".claude/prompts/foo.md", false},
		{"prompts/e2e-test-workflow.md", false},
		// Rejected — display labels
		{"chunk1", false},
		{"chunk2", false},
		{"chunk44", false},
		// Rejected — generic categories
		{"magic_strings", false},
		{"owasp_top10", false},
		{"single_responsibility_principle", false},
		// Rejected — empty/whitespace
		{"", false},
		{"   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.cite, func(t *testing.T) {
			got := isAcceptableCite(tc.cite)
			if got != tc.want {
				t.Errorf("isAcceptableCite(%q) = %v; want %v", tc.cite, got, tc.want)
			}
		})
	}
}
