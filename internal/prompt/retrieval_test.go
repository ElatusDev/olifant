package prompt

import (
	"reflect"
	"testing"
)

func TestSortHitsByDistance_AscendingOrder(t *testing.T) {
	hits := []Hit{
		{Distance: 0.9, Scope: "backend/corpus"},
		{Distance: 0.1, Scope: "webapp/corpus"},
		{Distance: 0.5, Scope: "infra/corpus"},
	}
	sortHitsByDistance(hits)
	want := []float32{0.1, 0.5, 0.9}
	for i, h := range hits {
		if h.Distance != want[i] {
			t.Errorf("hits[%d].Distance = %v, want %v", i, h.Distance, want[i])
		}
	}
}

func TestCapChars_ShortStringPassthrough(t *testing.T) {
	in := "hello"
	if got := capChars(in, 100); got != in {
		t.Errorf("capChars(%q, 100) = %q, want %q", in, got, in)
	}
}

func TestCapChars_TruncatesAtMaxLen(t *testing.T) {
	in := "abcdefghij" // 10 bytes
	got := capChars(in, 5)
	if got != "abcde" {
		t.Errorf("capChars %q to 5 = %q, want %q", in, got, "abcde")
	}
}

func TestCapChars_RespectsUTF8Boundary(t *testing.T) {
	// "héllo" — "é" is two bytes (0xc3 0xa9). Capping at 2 must back off to 1.
	in := "h\xc3\xa9llo"
	got := capChars(in, 2)
	if got != "h" {
		t.Errorf("capChars on utf8 boundary = %q, want %q", got, "h")
	}
	if got := capChars(in, 3); got != "h\xc3\xa9" {
		t.Errorf("capChars after multibyte char = %q, want %q", got, "h\xc3\xa9")
	}
}

func TestCapChars_AlreadyAtBoundary(t *testing.T) {
	// "héllo" — full string is 6 bytes; cap at 6 → unchanged.
	in := "h\xc3\xa9llo"
	if got := capChars(in, 6); got != in {
		t.Errorf("capChars at exact len = %q, want %q", got, in)
	}
}

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
