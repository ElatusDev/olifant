package prompt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildContext_HappyPath(t *testing.T) {
	oURL, cURL := retrievalServers(t)

	res, err := BuildContext(context.Background(), ContextConfig{
		Goal:      "tenant scoping",
		OllamaURL: oURL,
		ChromaURL: cURL,
		Embedder:  "m",
		Scopes:    []string{"backend"},
		TopN:      3,
	})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("no chunks returned")
	}
	c := res.Chunks[0]
	if c.Source != "patterns/backend.md" || c.Body != "chunk doc" {
		t.Errorf("chunk = %+v, want source patterns/backend.md body %q", c, "chunk doc")
	}
	if len(res.Sources) != 1 || res.Sources[0] != "patterns/backend.md" {
		t.Errorf("sources = %v", res.Sources)
	}
}

func TestBuildContext_ExtractsCitesAndCapsBody(t *testing.T) {
	oURL, _ := retrievalServers(t)
	longDoc := "Per D210 and AP164: " + strings.Repeat("x", 500)
	chr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = w.Write([]byte(`{"id":"coll-1","name":"corpus"}`))
		case strings.HasSuffix(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"ids":[["a"]],"documents":[["` + longDoc + `"]],"metadatas":[[{"source":"decisions/log.md","doc_type":"decision"}]],"distances":[[0.05]]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(chr.Close)

	res, err := BuildContext(context.Background(), ContextConfig{
		Goal:      "g",
		OllamaURL: oURL,
		ChromaURL: chr.URL,
		Embedder:  "m",
		Scopes:    []string{"backend"},
		TopN:      1,
		MaxChars:  40,
	})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	c := res.Chunks[0]
	if len(c.Cites) != 2 || c.Cites[0] != "AP164" || c.Cites[1] != "D210" {
		t.Errorf("cites = %v, want [AP164 D210] (cites extracted from full doc)", c.Cites)
	}
	if len(c.Body) > 45 { // CapChars is rune-safe; allow slack for the ellipsis
		t.Errorf("body not capped: %d chars", len(c.Body))
	}
	if c.DocType != "decision" {
		t.Errorf("doc_type = %q, want decision", c.DocType)
	}
}

func TestBuildContext_EmbedErrorPropagates(t *testing.T) {
	oll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(oll.Close)

	_, err := BuildContext(context.Background(), ContextConfig{
		Goal: "x", OllamaURL: oll.URL, ChromaURL: oll.URL, Embedder: "m",
		Scopes: []string{"backend"},
	})
	if err == nil {
		t.Fatal("want embed error, got nil")
	}
}
