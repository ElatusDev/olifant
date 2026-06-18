package retrieval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestCapChars(t *testing.T) {
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"short passthrough", "hello", 100, "hello"},
		{"ascii cap", "abcdefghij", 5, "abcde"},
		{"utf8 backs off inside rune", "h\xc3\xa9llo", 2, "h"},
		{"utf8 keeps full rune", "h\xc3\xa9llo", 3, "h\xc3\xa9"},
		{"exact len unchanged", "h\xc3\xa9llo", 6, "h\xc3\xa9llo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapChars(tc.in, tc.max); got != tc.want {
				t.Errorf("CapChars(%q,%d)=%q want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestSortByDistanceTruncate(t *testing.T) {
	hits := []Hit{
		{Distance: 0.9, Scope: "backend/corpus"},
		{Distance: 0.1, Scope: "webapp/corpus"},
		{Distance: 0.5, Scope: "infra/corpus"},
	}
	got := SortByDistanceTruncate(hits, 2)
	if len(got) != 2 {
		t.Fatalf("expected truncate to 2, got %d", len(got))
	}
	if got[0].Distance != 0.1 || got[1].Distance != 0.5 {
		t.Errorf("not sorted ascending: %+v", got)
	}
}

func TestSortByDistanceTruncate_ZeroTopNNoTruncate(t *testing.T) {
	hits := []Hit{{Distance: 0.5}, {Distance: 0.2}}
	got := SortByDistanceTruncate(hits, 0)
	if len(got) != 2 || got[0].Distance != 0.2 {
		t.Errorf("topN<=0 should sort but not truncate: %+v", got)
	}
}

// fakeBackend serves the Ollama embed path and Chroma collection/query paths
// so QueryScopedFamilies + Embed can be driven end-to-end without real services.
type fakeBackend struct {
	embedCalled int
	queryCalled int
}

func (f *fakeBackend) handler(w http.ResponseWriter, r *http.Request) {
	switch path := r.URL.Path; {
	case path == "/api/embed" && r.Method == http.MethodPost:
		f.embedCalled++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float32{{0.1, 0.2, 0.3}}})
	case strings.HasSuffix(path, "/query") && r.Method == http.MethodPost:
		f.queryCalled++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"documents": [][]string{{"chunk a", "chunk b"}},
			"distances": [][]float32{{0.10, 0.25}},
			"metadatas": [][]map[string]interface{}{{{"source": "a.yaml"}, {"source": "b.yaml"}}},
			"ids":       [][]string{{"id1", "id2"}},
		})
	case strings.HasSuffix(path, "/collections") && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "fake-id", "name": "fake"})
	case strings.HasSuffix(path, "/databases") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(path, "/tenants") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unhandled "+path, http.StatusNotFound)
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(http.HandlerFunc(fb.handler))
	defer srv.Close()

	qEmb, err := Embed(context.Background(), ollama.New(srv.URL), "m", "some query text", DefaultEmbedMaxChars)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(qEmb) != 1 || len(qEmb[0]) != 3 {
		t.Fatalf("unexpected embedding shape: %v", qEmb)
	}
	if fb.embedCalled != 1 {
		t.Errorf("embedCalled=%d want 1", fb.embedCalled)
	}
}

func TestQueryScopedFamilies_AlwaysVsCodeScopeGating(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(http.HandlerFunc(fb.handler))
	defer srv.Close()
	cc := chroma.New(srv.URL, "t", "d")
	qEmb := [][]float32{{0.1, 0.2, 0.3}}

	// challenge-style config: corpus + failure_modes always; code families
	// only for code scopes. universal is NOT a code scope.
	cfg := FamilyConfig{
		Families:       []string{"corpus", "code", "history", "code_history", "failure_modes"},
		AlwaysFamilies: map[string]bool{"corpus": true, "failure_modes": true},
		CodeScopes:     map[string]bool{"backend": true},
		Scopes:         []string{"universal", "backend"},
		TopN:           4,
	}
	hits := QueryScopedFamilies(context.Background(), cc, qEmb, cfg)

	// universal: corpus + failure_modes = 2 queries.
	// backend: all 5 families = 5 queries. Total 7.
	if fb.queryCalled != 7 {
		t.Fatalf("queryCalled=%d want 7 (2 universal + 5 backend)", fb.queryCalled)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	// Scope breadcrumb is "<scope>/<family>".
	for _, h := range hits {
		if !strings.Contains(h.Scope, "/") {
			t.Errorf("hit scope %q missing family breadcrumb", h.Scope)
		}
	}
}

func TestEmbed_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := Embed(context.Background(), ollama.New(srv.URL), "m", "q", DefaultEmbedMaxChars); err == nil ||
		!strings.Contains(err.Error(), "embed") {
		t.Fatalf("expected embed error, got %v", err)
	}
}

func TestEmbed_WrongVectorCount(t *testing.T) {
	// A non-1 embedding count surfaces as an error (the ollama client rejects
	// the count mismatch; Embed propagates it).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float32{{0.1}, {0.2}}})
	}))
	defer srv.Close()
	if _, err := Embed(context.Background(), ollama.New(srv.URL), "m", "q", DefaultEmbedMaxChars); err == nil ||
		!strings.Contains(err.Error(), "embed") {
		t.Fatalf("expected embed count-mismatch error, got %v", err)
	}
}

// TestQueryScopedFamilies_SkipsErrorsVerbose drives the per-collection
// error-skip + empty-result + verbose-log branches: ensure-collection failure,
// query failure, and zero documents must each be skipped without aborting.
func TestQueryScopedFamilies_SkipsErrorsVerbose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/tenants"), strings.HasSuffix(path, "/databases"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(path, "/query"):
			// Query failure path.
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(path, "/collections"):
			// corpus_backend resolves; everything else fails to ensure.
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "corpus_backend") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "ok", "name": "corpus_backend"})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.Error(w, "unhandled", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cc := chroma.New(srv.URL, "t", "d")
	hits := QueryScopedFamilies(context.Background(), cc, [][]float32{{0.1}}, FamilyConfig{
		Families:       []string{"corpus", "code"},
		AlwaysFamilies: map[string]bool{"corpus": true},
		CodeScopes:     map[string]bool{"backend": true},
		Scopes:         []string{"backend"},
		TopN:           4,
		Verbose:        true, // exercise the skip-log branches
	})
	// corpus_backend ensures but its query 500s; code_backend fails to ensure.
	// Both are skipped → no hits, no panic.
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits (all skipped), got %d", len(hits))
	}
}

func TestQueryScopedFamilies_CorpusOnlyForNonCodeScope(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(http.HandlerFunc(fb.handler))
	defer srv.Close()
	cc := chroma.New(srv.URL, "t", "d")

	cfg := FamilyConfig{
		Families:       []string{"corpus", "code", "history", "code_history"},
		AlwaysFamilies: map[string]bool{"corpus": true},
		CodeScopes:     map[string]bool{"backend": true},
		Scopes:         []string{"universal"},
		TopN:           4,
	}
	QueryScopedFamilies(context.Background(), cc, [][]float32{{0.1}}, cfg)
	if fb.queryCalled != 1 {
		t.Fatalf("queryCalled=%d want 1 (corpus only for universal)", fb.queryCalled)
	}
}
