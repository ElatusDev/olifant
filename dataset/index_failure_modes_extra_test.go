package dataset

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestGroupFailureModesByScope_SkipsAndBuckets(t *testing.T) {
	entries := []failureModeEntry{
		{ID: "fm1", Code: "C1", Scope: "backend", UserPrompt: "Q1", CorrectAssistantResponse: "A1", Rationale: "R1", Cite: "D154"},
		{ID: "fm2", Scope: "", UserPrompt: "Q2", CorrectAssistantResponse: "A2"},     // empty scope → universal
		{ID: "fm3", Scope: "webapp", UserPrompt: "", CorrectAssistantResponse: "A3"}, // no prompt → skipped
		{ID: "fm4", Scope: "webapp", UserPrompt: "Q4", CorrectAssistantResponse: ""}, // no answer → skipped
	}
	out := groupFailureModesByScope(entries, "failure-modes/v1.yaml")

	if len(out["backend"]) != 1 || len(out["universal"]) != 1 {
		t.Fatalf("buckets = %v, want backend:1 universal:1", map[string]int{"backend": len(out["backend"]), "universal": len(out["universal"])})
	}
	if len(out["webapp"]) != 0 {
		t.Errorf("webapp should be empty (both entries skipped), got %d", len(out["webapp"]))
	}

	be := out["backend"][0]
	if be.Metadata["origin_cite"] != "D154" {
		t.Errorf("origin_cite not captured: %v", be.Metadata)
	}
	if be.Metadata["source"] != "failure-modes/v1.yaml#fm1" {
		t.Errorf("source meta = %v", be.Metadata["source"])
	}
	for _, want := range []string{"Q: Q1", "A: A1", "Rationale: R1"} {
		if !strings.Contains(be.Body, want) {
			t.Errorf("composed body missing %q:\n%s", want, be.Body)
		}
	}
}

func TestChunkID_Stable(t *testing.T) {
	a := chunkID("fm", "x")
	if a != chunkID("fm", "x") {
		t.Error("chunkID not deterministic")
	}
	if a == chunkID("fm", "y") {
		t.Error("chunkID ignored key change")
	}
}

func TestLoadFailureModeEntries(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "v1.yaml")
	_ = os.WriteFile(p, []byte(`meta:
  version: 1
  source: x
entries:
  - id: fm1
    scope: backend
    user_prompt: Q
    correct_assistant_response: A
`), 0o644)
	entries, err := loadFailureModeEntries(p)
	if err != nil {
		t.Fatalf("loadFailureModeEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "fm1" {
		t.Errorf("entries = %+v", entries)
	}

	if _, err := loadFailureModeEntries(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file should error")
	}

	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("entries: [unterminated"), 0o644)
	if _, err := loadFailureModeEntries(bad); err == nil {
		t.Error("invalid yaml should error")
	}
}

func TestEmbedAndUpsertFM(t *testing.T) {
	oc := ollama.New(newFakeOllama(t))
	upserts := 0
	cc := chroma.New(newFakeChroma(t, &upserts), "", "")

	chunks := []failureModeChunk{
		{ID: "a", Body: "body a", Metadata: map[string]interface{}{"scope": "backend"}},
		{ID: "b", Body: "body b", Metadata: map[string]interface{}{"scope": "backend"}},
		{ID: "c", Body: "body c", Metadata: map[string]interface{}{"scope": "backend"}},
	}
	ups, batches, err := embedAndUpsertFM(context.Background(), oc, cc, "coll-1", "bge-m3", chunks, 2)
	if err != nil {
		t.Fatalf("embedAndUpsertFM: %v", err)
	}
	if ups != 3 {
		t.Errorf("upserted = %d, want 3", ups)
	}
	if batches != 2 { // 2 + 1
		t.Errorf("batches = %d, want 2", batches)
	}
	if upserts != 3 {
		t.Errorf("chroma saw %d ids, want 3", upserts)
	}
}

func TestEmbedAndUpsertFM_EmbedError(t *testing.T) {
	// Ollama server that 500s on embed → error surfaces.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	oc := ollama.New(srv.URL)
	cc := chroma.New(srv.URL, "", "")
	_, _, err := embedAndUpsertFM(context.Background(), oc, cc, "c", "m",
		[]failureModeChunk{{ID: "a", Body: "x"}}, 8)
	if err == nil {
		t.Error("embed failure should propagate")
	}
}

// newFakeOllama returns one fixed vector per embed input.
func newFakeOllama(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req ollama.EmbedRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			embs := make([][]float32, len(req.Input))
			for i := range embs {
				embs[i] = []float32{0.1, 0.2}
			}
			_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: embs})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newFakeChroma(t *testing.T, upserts *int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upsert") {
			var req chroma.UpsertRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			*upserts += len(req.IDs)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
