package corpus

import (
	"reflect"
	"testing"
)

func TestSelectKinds(t *testing.T) {
	cases := []struct {
		name        string
		in          []string
		wantVocab   bool
		wantProse   bool
	}{
		{"empty -> both", nil, true, true},
		{"empty slice -> both", []string{}, true, true},
		{"vocab only", []string{"vocab"}, true, false},
		{"prose only", []string{"prose"}, false, true},
		{"both", []string{"vocab", "prose"}, true, true},
		{"whitespace + case", []string{" Vocab ", "PROSE"}, true, true},
		{"unknown ignored", []string{"junk"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotV, gotP := selectKinds(tc.in)
			if gotV != tc.wantVocab || gotP != tc.wantProse {
				t.Fatalf("selectKinds(%v) = (%v,%v); want (%v,%v)",
					tc.in, gotV, gotP, tc.wantVocab, tc.wantProse)
			}
		})
	}
}

func TestDeriveVocabRepoSlice(t *testing.T) {
	cases := []struct {
		name      string
		root      string
		file      string
		wantRepo  string
		wantSlice string
	}{
		{
			name:      "core-api flat",
			root:      "/kb/corpus/v2-curriculum/vocab",
			file:      "/kb/corpus/v2-curriculum/vocab/core-api/application.yaml",
			wantRepo:  "core-api",
			wantSlice: "application",
		},
		{
			name:      "webapp features subdir",
			root:      "/kb/corpus/v2-curriculum/vocab",
			file:      "/kb/corpus/v2-curriculum/vocab/akademia-plus-web/features/admin-ops.yaml",
			wantRepo:  "akademia-plus-web",
			wantSlice: "features/admin-ops",
		},
		{
			name:      "knowledge-base sub-dir",
			root:      "/kb/corpus/v2-curriculum/vocab",
			file:      "/kb/corpus/v2-curriculum/vocab/knowledge-base/decisions.yaml",
			wantRepo:  "knowledge-base",
			wantSlice: "decisions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRepo, gotSlice := deriveVocabRepoSlice(tc.root, tc.file)
			if gotRepo != tc.wantRepo || gotSlice != tc.wantSlice {
				t.Fatalf("deriveVocabRepoSlice = (%q,%q); want (%q,%q)",
					gotRepo, gotSlice, tc.wantRepo, tc.wantSlice)
			}
		})
	}
}

func TestDeriveProseRepoSlice(t *testing.T) {
	cases := []struct {
		name      string
		root      string
		file      string
		wantRepo  string
		wantSlice string
	}{
		{
			name:      "single-repo prose",
			root:      "/kb/corpus/v2-curriculum/prose",
			file:      "/kb/corpus/v2-curriculum/prose/elatusdev-web.yaml",
			wantRepo:  "elatusdev-web",
			wantSlice: "",
		},
		{
			name:      "knowledge-base sub-slice",
			root:      "/kb/corpus/v2-curriculum/prose",
			file:      "/kb/corpus/v2-curriculum/prose/knowledge-base/dictionary.yaml",
			wantRepo:  "knowledge-base",
			wantSlice: "dictionary",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRepo, gotSlice := deriveProseRepoSlice(tc.root, tc.file)
			if gotRepo != tc.wantRepo || gotSlice != tc.wantSlice {
				t.Fatalf("deriveProseRepoSlice = (%q,%q); want (%q,%q)",
					gotRepo, gotSlice, tc.wantRepo, tc.wantSlice)
			}
		})
	}
}

func TestNamespacedID(t *testing.T) {
	cases := []struct {
		in   v2Item
		want string
	}{
		{v2Item{ID: "SYM-abc", ItemKind: "symbol"}, "symbol-SYM-abc"},
		{v2Item{ID: "SYM-def", ItemKind: "sentence"}, "sentence-SYM-def"},
	}
	for _, tc := range cases {
		got := namespacedID(tc.in)
		if got != tc.want {
			t.Fatalf("namespacedID(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestV2MetadataForChroma_ScalarPassthrough(t *testing.T) {
	it := v2Item{
		ID:       "SYM-abc",
		Source:   "application/src/main/java/com/akademiaplus/Main.java",
		Line:     14,
		Repo:     "core-api",
		Slice:    "application",
		ItemKind: "symbol",
		Tags: map[string]any{
			"kind":     "class",
			"language": "java",
			"scope":    "backend",
		},
	}
	got := v2MetadataForChroma(it)
	for k, v := range map[string]any{
		"item_kind": "symbol",
		"source":    "application/src/main/java/com/akademiaplus/Main.java",
		"line":      14,
		"repo":      "core-api",
		"slice":     "application",
		"item_id":   "SYM-abc",
		"kind":      "class",
		"language":  "java",
		"scope":     "backend",
	} {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("metadata[%q] = %v (%T); want %v", k, got[k], got[k], v)
		}
	}
}

func TestV2MetadataForChroma_MultiValuedConcernJoins(t *testing.T) {
	it := v2Item{
		ID:       "SYM-xyz",
		ItemKind: "symbol",
		Tags: map[string]any{
			// Concern as []string (post-yaml-unmarshal into typed slice)
			"concern": []string{"security", "persistence"},
			"kind":    "class",
		},
	}
	got := v2MetadataForChroma(it)
	if got["concern"] != "security,persistence" {
		t.Errorf("typed []string concern = %v; want comma-joined", got["concern"])
	}

	// yaml.v3 unmarshal into map[string]any produces []any, not []string.
	it2 := v2Item{
		ID:       "SYM-xyz",
		ItemKind: "symbol",
		Tags: map[string]any{
			"concern": []any{"security", "tenancy"},
			"kind":    "class",
		},
	}
	got2 := v2MetadataForChroma(it2)
	if got2["concern"] != "security,tenancy" {
		t.Errorf("[]any concern = %v; want comma-joined", got2["concern"])
	}
}

func TestV2MetadataForChroma_EmptyAndNilSkipped(t *testing.T) {
	it := v2Item{
		ID:       "SYM-1",
		ItemKind: "sentence",
		Tags: map[string]any{
			"kind":     "",        // empty string skipped
			"language": "markdown",
			"":         "ignored", // empty key skipped
			"concern":  []any{},   // empty list skipped
			"nilval":   nil,       // nil skipped
		},
	}
	got := v2MetadataForChroma(it)
	if _, ok := got["kind"]; ok {
		t.Error("empty kind should be skipped")
	}
	if _, ok := got[""]; ok {
		t.Error("empty key should be skipped")
	}
	if _, ok := got["concern"]; ok {
		t.Error("empty concern list should be skipped")
	}
	if _, ok := got["nilval"]; ok {
		t.Error("nil tag value should be skipped")
	}
	if got["language"] != "markdown" {
		t.Errorf("language passthrough lost: %v", got["language"])
	}
}

func TestTruncID(t *testing.T) {
	if got := truncID("short"); got != "short" {
		t.Errorf("short returned %q", got)
	}
	in := "SYM-abcdef0123456789longtail"
	got := truncID(in)
	if len(got) != 16 || got != "SYM-abcdef012345" {
		t.Errorf("truncID(%q) = %q (len=%d); want 16-char prefix",
			in, got, len(got))
	}
}
