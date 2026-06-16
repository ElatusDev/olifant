package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/challenge"
	"gopkg.in/yaml.v3"
)

func TestWriteCaseMeta_retrievedHits(t *testing.T) {
	dir := t.TempDir()
	res := &challenge.Result{
		RetrievedCount: 2,
		RetrievedHits: []challenge.HitSummary{
			{Rank: 1, Distance: 0.05, Scope: "mobile/code", Source: "SettingsScreen.tsx", DocSnippet: "export function Settings"},
			{Rank: 2, Distance: 0.23, Scope: "universal/failure_modes", Source: "FM6"},
		},
	}
	writeCaseMeta(dir, Case{ID: "case1", Scope: []string{"mobile"}, File: "x"}, res)

	body, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta map[string]interface{}
	if err := yaml.Unmarshal(body, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rh, ok := meta["retrieved_hits"].([]interface{})
	if !ok || len(rh) != 2 {
		t.Fatalf("retrieved_hits = %#v, want 2 entries", meta["retrieved_hits"])
	}
	first, _ := rh[0].(map[string]interface{})
	if first["rank"] != 1 || first["scope"] != "mobile/code" || first["source"] != "SettingsScreen.tsx" {
		t.Errorf("first hit = %#v", first)
	}
	// snake_case key rendering from the HitSummary yaml tags.
	if !strings.Contains(string(body), "doc_snippet:") {
		t.Errorf("expected doc_snippet key, got:\n%s", body)
	}
}

func TestWriteCaseMeta_noRetrievedHits(t *testing.T) {
	dir := t.TempDir()
	writeCaseMeta(dir, Case{ID: "c", Scope: []string{"s"}, File: "f"}, &challenge.Result{})

	body, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if strings.Contains(string(body), "retrieved_hits") {
		t.Errorf("retrieved_hits should be absent when empty:\n%s", body)
	}
}
