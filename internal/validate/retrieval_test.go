package validate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBackend is a single httptest.Server that handles both the Ollama
// embed path and the Chroma tenant/database/collection/query paths so we
// can drive Retrieve() end-to-end without spinning up real services.
type fakeBackend struct {
	t            *testing.T
	embedCalled  int
	queryCalled  int
	queryRequest []byte
}

func (f *fakeBackend) handler(w http.ResponseWriter, r *http.Request) {
	// Order matters: most-specific suffixes first, fall through to broader matches.
	path := r.URL.Path
	switch {
	case path == "/api/embed" && r.Method == http.MethodPost:
		f.embedCalled++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"embeddings": [][]float32{{0.1, 0.2, 0.3}},
		})

	case strings.HasSuffix(path, "/query") && r.Method == http.MethodPost:
		f.queryCalled++
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		f.queryRequest = buf
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"documents": [][]string{{"chunk one about TenantScoped", "chunk two about D17"}},
			"distances": [][]float32{{0.10, 0.25}},
			"metadatas": [][]map[string]interface{}{{
				{"source": "knowledge-base/concepts/backend/concepts.yaml", "artifact_id": "TenantScoped"},
				{"source": "knowledge-base/decisions/log.yaml#D17", "artifact_id": "D17"},
			}},
			"ids": [][]string{{"id1", "id2"}},
		})

	case strings.HasSuffix(path, "/collections") && r.Method == http.MethodPost:
		// EnsureCollection POST — return a fake collection.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "fake-collection-id",
			"name": "fake",
		})

	case strings.HasSuffix(path, "/databases") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusOK)

	case strings.HasSuffix(path, "/tenants") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "unhandled "+path, http.StatusNotFound)
	}
}

func TestRetrieve_HappyPath_ReturnsHitsSortedByDistance(t *testing.T) {
	fb := &fakeBackend{t: t}
	srv := httptest.NewServer(http.HandlerFunc(fb.handler))
	defer srv.Close()

	hits, err := Retrieve(context.Background(), RetrievalConfig{
		OllamaURL: srv.URL,
		ChromaURL: srv.URL,
		Embedder:  "fake-embed",
		Tenant:    "tt",
		Database:  "dd",
		Scopes:    []string{"backend"}, // backend = codeScopes member, triggers all 4 families
		TopN:      4,
	}, "added tenant-scoped composite key for invoices")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected hits, got none")
	}
	if fb.embedCalled != 1 {
		t.Fatalf("expected exactly 1 embed call, got %d", fb.embedCalled)
	}
	// 4 families × 1 scope = 4 queries.
	if fb.queryCalled != 4 {
		t.Fatalf("expected 4 query calls (one per family), got %d", fb.queryCalled)
	}
	// Globally capped at TopN.
	if len(hits) > 4 {
		t.Fatalf("expected at most 4 hits, got %d", len(hits))
	}
	// Sorted ascending by distance.
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Distance > hits[i].Distance {
			t.Fatalf("hits not sorted: hit[%d]=%f > hit[%d]=%f", i-1, hits[i-1].Distance, i, hits[i].Distance)
		}
	}
}

func TestRetrieve_NonCodeScope_SkipsNonCorpusFamilies(t *testing.T) {
	fb := &fakeBackend{t: t}
	srv := httptest.NewServer(http.HandlerFunc(fb.handler))
	defer srv.Close()

	_, err := Retrieve(context.Background(), RetrievalConfig{
		OllamaURL: srv.URL,
		ChromaURL: srv.URL,
		Embedder:  "fake-embed",
		Tenant:    "tt",
		Database:  "dd",
		Scopes:    []string{"universal"}, // not in codeScopes — should only hit corpus
		TopN:      4,
	}, "anything")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// 1 family (corpus only) × 1 scope = 1 query.
	if fb.queryCalled != 1 {
		t.Fatalf("expected 1 query (corpus only), got %d", fb.queryCalled)
	}
}

func TestRenderRetrievedBlock_EmptyHits_ReturnsEmpty(t *testing.T) {
	if got := renderRetrievedBlock(nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRenderRetrievedBlock_NonEmptyHits_IncludesSources(t *testing.T) {
	hits := []RetrievedHit{
		{
			Doc: "tenant rules",
			Meta: map[string]interface{}{
				"source":      "knowledge-base/anti-patterns/catalog.yaml#AP3",
				"artifact_id": "AP3",
			},
			Distance: 0.1,
			Scope:    "backend/corpus",
		},
	}
	got := renderRetrievedBlock(hits)
	for _, want := range []string{
		"RETRIEVED CONTEXT",
		"AP3",
		"backend/corpus",
		"tenant rules",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered block missing %q, got:\n%s", want, got)
		}
	}
}

func TestCapChars_AsciiAndUTF8(t *testing.T) {
	if got := capChars("hello", 10); got != "hello" {
		t.Fatalf("under-cap should pass through, got %q", got)
	}
	if got := capChars("hello world", 5); got != "hello" {
		t.Fatalf("ascii cap, got %q", got)
	}
	// UTF-8 — cap inside a multibyte rune should rewind to boundary.
	s := "héllo" // é = 2 bytes
	// cap=2 → byte 1 is 0x68 ('h'), byte 2 is 0xC3 (start of é) — would land inside é.
	got := capChars(s, 2)
	if len(got) > 2 {
		t.Fatalf("expected <=2 bytes, got %d (%q)", len(got), got)
	}
	// The result must be valid UTF-8 (no truncated rune).
	for i, r := range got {
		if r == 0xFFFD { // replacement char from broken UTF-8
			t.Fatalf("capChars produced invalid UTF-8 at byte %d", i)
		}
	}
}
