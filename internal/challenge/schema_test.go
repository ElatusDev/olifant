package challenge

import (
	"regexp"
	"testing"
)

func TestCitePathPattern(t *testing.T) {
	prefixes := []string{"core-api", "elatusdev-web", "knowledge-base"}
	re := regexp.MustCompile(citePathPattern(prefixes))

	match := []string{
		"core-api/security/src/main/java/com/akademiaplus/Foo.java",
		"core-api/security/src/main/java/com/akademiaplus/Foo.java#L1-L80",
		"knowledge-base/decisions/log.yaml#D17",
		"elatusdev-web/src/features/akademiaPlus/pages/DemoRequestPage.tsx#L1-L57",
		// stale-but-prefix-anchored: structurally valid; nonexistence is the
		// validator's job, not the grammar's.
		"core-api/task-management/src/main/java/Foo.java",
	}
	for _, m := range match {
		if !re.MatchString(m) {
			t.Errorf("expected path pattern to MATCH %q", m)
		}
	}

	noMatch := []string{
		"artifact_id=02d73c5",   // composite token — the invention we block
		"platform/CLAUDE.md",    // platform/ is not a whitelisted prefix
		"CLAUDE.md",             // bare root file, no prefix
		"D17",                   // non-path token (belongs in the enum alt)
		"randomrepo/Foo.java",   // unwhitelisted prefix
		"core-api/security/Foo", // no extension
	}
	for _, n := range noMatch {
		if re.MatchString(n) {
			t.Errorf("expected path pattern to REJECT %q", n)
		}
	}
}

func TestCitePathPattern_emptyPrefixes(t *testing.T) {
	if got := citePathPattern(nil); got != "" {
		t.Errorf("citePathPattern(nil) = %q, want empty", got)
	}
}

func TestCitesSchema_fallbackWhenNoPattern(t *testing.T) {
	// Empty pattern → free stringArray (static schema / nil-validator path).
	got := citesSchema("", []string{"D17"}, citeMaxItems)
	if _, hasItems := got["items"]; !hasItems {
		t.Fatal("expected stringArray with items")
	}
	if _, hasMax := got["maxItems"]; hasMax {
		t.Error("stringArray fallback should not set maxItems")
	}
	items, _ := got["items"].(map[string]interface{})
	if items["type"] != "string" {
		t.Errorf("fallback items type = %v, want plain string array", items["type"])
	}
}

func TestCitesSchema_hybridAnyOf(t *testing.T) {
	got := citesSchema("^(core-api)/.*$", []string{"D17", "AP3"}, citeMaxItems)
	if got["maxItems"] != citeMaxItems {
		t.Errorf("maxItems = %v, want %d", got["maxItems"], citeMaxItems)
	}
	items, _ := got["items"].(map[string]interface{})
	alts, ok := items["anyOf"].([]interface{})
	if !ok || len(alts) != 2 {
		t.Fatalf("expected anyOf with 2 alts, got %#v", items["anyOf"])
	}
	pathAlt, _ := alts[0].(map[string]interface{})
	if pathAlt["pattern"] != "^(core-api)/.*$" {
		t.Errorf("path alt pattern = %v", pathAlt["pattern"])
	}
	enumAlt, _ := alts[1].(map[string]interface{})
	enum, _ := enumAlt["enum"].([]string)
	if len(enum) != 2 {
		t.Errorf("enum alt = %v, want [D17 AP3]", enumAlt["enum"])
	}
}

func TestCitesSchema_pathOnlyWhenEnumEmpty(t *testing.T) {
	got := citesSchema("^(core-api)/.*$", nil, citeMaxItems)
	items, _ := got["items"].(map[string]interface{})
	alts, _ := items["anyOf"].([]interface{})
	if len(alts) != 1 {
		t.Errorf("expected single path alt when enum empty, got %d", len(alts))
	}
}

func TestNonPathCiteTerms_dropsPathsAndDedups(t *testing.T) {
	v := &CiteValidator{
		termsByLayerScope: map[Layer]map[string][]string{
			LayerDictionary: {
				ApexScope: {"D17", "AP3"},
				"backend": {"SB-04", "D17"}, // D17 duplicate across scopes
			},
			LayerConcept: {
				"backend": {"NewsFeed", "core-api/x/Foo.java"}, // path-shaped → dropped
			},
		},
	}
	got := v.nonPathCiteTerms([]string{"backend"})

	want := map[string]bool{"D17": true, "AP3": true, "SB-04": true, "NewsFeed": true}
	if len(got) != len(want) {
		t.Fatalf("nonPathCiteTerms = %v, want keys %v", got, want)
	}
	for _, term := range got {
		if !want[term] {
			t.Errorf("unexpected term %q (path-shaped terms should be dropped)", term)
		}
	}
}
