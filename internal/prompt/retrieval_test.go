package prompt

import (
	"reflect"
	"testing"
)

func TestSourcePathsFromHits_DedupePreservesOrder(t *testing.T) {
	hits := []Hit{
		{Meta: map[string]interface{}{"source": "a.md"}},
		{Meta: map[string]interface{}{"source": "b.md"}},
		{Meta: map[string]interface{}{"source": "a.md"}}, // duplicate
		{Meta: map[string]interface{}{"source": "c.md"}},
	}
	got := sourcePathsFromHits(hits)
	want := []string{"a.md", "b.md", "c.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sourcePathsFromHits = %v, want %v", got, want)
	}
}

func TestSourcePathsFromHits_SkipsMissingOrEmpty(t *testing.T) {
	hits := []Hit{
		{Meta: map[string]interface{}{"source": ""}},
		{Meta: map[string]interface{}{}}, // missing key
		{Meta: map[string]interface{}{"source": "x.md"}},
		{Meta: map[string]interface{}{"source": 42}}, // wrong type
	}
	got := sourcePathsFromHits(hits)
	want := []string{"x.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sourcePathsFromHits skip-bad = %v, want %v", got, want)
	}
}

func TestAllScopes_ContainsExpectedSet(t *testing.T) {
	want := map[string]bool{
		"universal": true, "backend": true, "webapp": true,
		"mobile": true, "e2e": true, "infra": true, "platform-process": true,
	}
	if len(allScopes) != len(want) {
		t.Fatalf("allScopes has %d entries, want %d", len(allScopes), len(want))
	}
	for _, s := range allScopes {
		if !want[s] {
			t.Errorf("unexpected scope in allScopes: %s", s)
		}
	}
}

func TestCodeScopes_ExcludesUniversalAndPlatformProcess(t *testing.T) {
	if codeScopes["universal"] {
		t.Errorf("codeScopes must NOT include 'universal' (no code ingested there)")
	}
	if codeScopes["platform-process"] {
		t.Errorf("codeScopes must NOT include 'platform-process'")
	}
	for _, s := range []string{"backend", "webapp", "mobile", "e2e", "infra"} {
		if !codeScopes[s] {
			t.Errorf("codeScopes missing %q", s)
		}
	}
}
