package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	if got := extractJSONObject("prefix {\"a\":1} suffix"); got != `{"a":1}` {
		t.Errorf("extractJSONObject = %q", got)
	}
	// No braces → returns input unchanged.
	if got := extractJSONObject("no json here"); got != "no json here" {
		t.Errorf("no-brace passthrough = %q", got)
	}
}

func TestMergeConcerns(t *testing.T) {
	// []string existing + llm, deduped.
	got := mergeConcerns([]string{"security", "tenancy"}, []string{"tenancy", "testing"})
	if strings.Join(got, ",") != "security,tenancy,testing" {
		t.Errorf("merge = %v", got)
	}
	// []any existing (as decoded from YAML).
	got2 := mergeConcerns([]any{"ui", "api-contract"}, []string{"ui"})
	if strings.Join(got2, ",") != "ui,api-contract" {
		t.Errorf("merge []any = %v", got2)
	}
	// Non-slice existing → only llm survives; empty strings dropped.
	got3 := mergeConcerns(nil, []string{"", "build"})
	if strings.Join(got3, ",") != "build" {
		t.Errorf("merge nil existing = %v", got3)
	}
}

func TestSafeSlices(t *testing.T) {
	ss := [][]string{{"a"}, {"b"}}
	if got := safeStringSlice(ss, 1); len(got) != 1 || got[0] != "b" {
		t.Errorf("safeStringSlice in-range = %v", got)
	}
	if got := safeStringSlice(ss, 9); got != nil {
		t.Errorf("safeStringSlice out-of-range = %v, want nil", got)
	}

	ms := [][]map[string]interface{}{{{"k": "v"}}}
	if got := safeMetaSlice(ms, 0); len(got) != 1 {
		t.Errorf("safeMetaSlice in-range = %v", got)
	}
	if got := safeMetaSlice(ms, 5); got != nil {
		t.Errorf("safeMetaSlice out-of-range = %v", got)
	}

	fs := [][]float32{{0.1}}
	if got := safeFloatSlice(fs, 0); len(got) != 1 {
		t.Errorf("safeFloatSlice in-range = %v", got)
	}
	if got := safeFloatSlice(fs, 9); got != nil {
		t.Errorf("safeFloatSlice out-of-range = %v", got)
	}
}

func TestWriteProseYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prose.yaml")
	sentences := []Sentence{
		{ID: "s1", Text: "first sentence", Source: "a.md", Line: 1},
		{ID: "s2", Text: "second sentence", Source: "a.md", Line: 2},
	}
	if err := writeProseYAML(p, sentences); err != nil {
		t.Fatalf("writeProseYAML: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read prose: %v", err)
	}
	for _, want := range []string{"id: s1", "first sentence", "id: s2"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("prose yaml missing %q:\n%s", want, raw)
		}
	}
}

func TestWriteSmokeReport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "smoke.md")
	results := []SmokeResult{
		{Query: "tenant scoping", Hits: []SmokeHit{
			{ID: "1", Distance: 0.12, Text: "use @SQLDelete with tenant_id", Repo: "core-api", ItemKind: "rule", Source: "x.md"},
		}},
		{Query: "no hits query", Hits: nil},
	}
	if err := writeSmokeReport(p, "kb_v2", results); err != nil {
		t.Fatalf("writeSmokeReport: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read smoke: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "kb_v2") || !strings.Contains(s, "tenant scoping") {
		t.Errorf("smoke report missing header/query:\n%s", s)
	}
	if !strings.Contains(s, "_(no hits)_") {
		t.Errorf("empty-hits section not rendered:\n%s", s)
	}
}
