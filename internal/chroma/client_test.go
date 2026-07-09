package chroma

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingServer spins up an httptest server whose handler is supplied per
// test, and exposes a client pointed at it.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "", "")
	return c, srv
}

func TestNew_Defaults(t *testing.T) {
	c := New("http://host:8000/", "", "")
	if c.Tenant != "default_tenant" || c.Database != "default_database" {
		t.Errorf("defaults not applied: tenant=%q db=%q", c.Tenant, c.Database)
	}
	if c.BaseURL != "http://host:8000" {
		t.Errorf("trailing slash not trimmed: %q", c.BaseURL)
	}
	if c.HTTP == nil {
		t.Error("HTTP client not initialised")
	}
}

func TestNew_ExplicitTenantDatabase(t *testing.T) {
	c := New("http://h", "t1", "d1")
	if c.Tenant != "t1" || c.Database != "d1" {
		t.Errorf("explicit values overridden: tenant=%q db=%q", c.Tenant, c.Database)
	}
}

func TestHeartbeat(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/heartbeat" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"nanosecond heartbeat": 1717000000000000000}`)
	})
	hb, err := c.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb != 1717000000000000000 {
		t.Errorf("heartbeat = %d", hb)
	}
}

func TestHeartbeat_ServerError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := c.Heartbeat(context.Background()); err == nil {
		t.Fatal("want error on 500, got nil")
	}
}

func TestEnsureTenant_CreatesAndPostsName(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/tenants" || r.Method != http.MethodPost {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "default_tenant" {
			t.Errorf("tenant name = %q", body["name"])
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := c.EnsureTenant(context.Background()); err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
}

func TestEnsureTenant_AlreadyExistsIsNoError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tenant already exists", http.StatusConflict)
	})
	if err := c.EnsureTenant(context.Background()); err != nil {
		t.Errorf("409 should be swallowed, got %v", err)
	}
}

func TestEnsureTenant_RealErrorPropagates(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})
	if err := c.EnsureTenant(context.Background()); err == nil {
		t.Error("502 should propagate, got nil")
	}
}

func TestEnsureDatabase_PathIncludesTenant(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/tenants/t1/databases" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}, "t1", "d1")
	if err := c.EnsureDatabase(context.Background()); err != nil {
		t.Fatalf("EnsureDatabase: %v", err)
	}
}

func TestEnsureDatabase_AlreadyExistsByBody(t *testing.T) {
	// 400 status but "already exists" body — exercises the body-string branch.
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Database already exists", http.StatusBadRequest)
	}, "t1", "d1")
	if err := c.EnsureDatabase(context.Background()); err != nil {
		t.Errorf("already-exists body should be swallowed, got %v", err)
	}
}

func TestEnsureCollection_DefaultMetadataAndParse(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/tenants/t1/databases/d1/collections"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		md, _ := body["metadata"].(map[string]interface{})
		if md["hnsw:space"] != "cosine" {
			t.Errorf("default metadata missing cosine: %v", body["metadata"])
		}
		if body["get_or_create"] != true {
			t.Errorf("get_or_create not set")
		}
		_, _ = io.WriteString(w, `{"id":"col-123","name":"corpus"}`)
	}, "t1", "d1")

	col, err := c.EnsureCollection(context.Background(), "corpus", nil)
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if col.ID != "col-123" || col.Name != "corpus" {
		t.Errorf("parsed collection = %+v", col)
	}
}

func TestEnsureCollection_ErrorReturnsNil(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}, "t1", "d1")
	col, err := c.EnsureCollection(context.Background(), "corpus", map[string]interface{}{"x": 1})
	if err == nil || col != nil {
		t.Errorf("want (nil, err), got (%v, %v)", col, err)
	}
}

func TestCount(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/tenants/t1/databases/d1/collections/col-1/count"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		_, _ = io.WriteString(w, `42`)
	}, "t1", "d1")
	n, err := c.Count(context.Background(), "col-1")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 42 {
		t.Errorf("count = %d", n)
	}
}

func TestCount_ErrorReturnsZero(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "x", http.StatusInternalServerError)
	}, "t1", "d1")
	if n, err := c.Count(context.Background(), "col-1"); err == nil || n != 0 {
		t.Errorf("want (0, err), got (%d, %v)", n, err)
	}
}

func TestUpsert_EmptyIDsShortCircuits(t *testing.T) {
	called := false
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	}, "t1", "d1")
	if err := c.Upsert(context.Background(), "col-1", UpsertRequest{}); err != nil {
		t.Fatalf("Upsert empty: %v", err)
	}
	if called {
		t.Error("empty upsert should not hit the server")
	}
}

func TestUpsert_SendsPayload(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/tenants/t1/databases/d1/collections/col-1/upsert"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		var req UpsertRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.IDs) != 1 || req.IDs[0] != "id-1" {
			t.Errorf("ids = %v", req.IDs)
		}
		w.WriteHeader(http.StatusCreated)
	}, "t1", "d1")
	req := UpsertRequest{
		IDs:        []string{"id-1"},
		Embeddings: [][]float32{{0.1, 0.2}},
		Documents:  []string{"doc"},
		Metadatas:  []map[string]interface{}{{"scope": "backend"}},
	}
	if err := c.Upsert(context.Background(), "col-1", req); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestQuery_DefaultIncludeAndParse(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v2/tenants/t1/databases/d1/collections/col-1/query"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		var req QueryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Include) != 3 {
			t.Errorf("default Include not applied: %v", req.Include)
		}
		_, _ = io.WriteString(w, `{"ids":[["a"]],"documents":[["doc-a"]],"distances":[[0.12]]}`)
	}, "t1", "d1")

	resp, err := c.Query(context.Background(), "col-1", QueryRequest{
		QueryEmbeddings: [][]float32{{0.1}},
		NResults:        1,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.IDs) != 1 || resp.IDs[0][0] != "a" {
		t.Errorf("ids = %v", resp.IDs)
	}
	if resp.Distances[0][0] != 0.12 {
		t.Errorf("distances = %v", resp.Distances)
	}
}

func TestQuery_RespectsExplicitInclude(t *testing.T) {
	c := newClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var req QueryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Include) != 1 || req.Include[0] != "distances" {
			t.Errorf("explicit Include overridden: %v", req.Include)
		}
		_, _ = io.WriteString(w, `{"ids":[["a"]]}`)
	}, "t1", "d1")
	_, err := c.Query(context.Background(), "col-1", QueryRequest{Include: []string{"distances"}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
}

func TestHTTPError_MessageShape(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaboom", http.StatusTeapot)
	})
	_, err := c.Heartbeat(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	for _, want := range []string{"chroma", "GET", "/api/v2/heartbeat", "418", "kaboom"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestDo_RequestBuildError(t *testing.T) {
	// A control char in the path makes http.NewRequestWithContext fail before
	// any network call, exercising the request-build error branch.
	c := New("http://example.invalid", "t1", "d1")
	err := c.do(context.Background(), "GET\n", "/x", nil, nil)
	if err == nil {
		t.Error("want request-build error, got nil")
	}
}

func TestIsAlreadyExists_NonHTTPError(t *testing.T) {
	if isAlreadyExists(nil) {
		t.Error("nil should not be already-exists")
	}
	if isAlreadyExists(io.EOF) {
		t.Error("plain error should not be already-exists")
	}
	if isAlreadyExists(&httpError{Status: http.StatusInternalServerError, Body: "x"}) {
		t.Error("500 with unrelated body should not be already-exists")
	}
}

// newClientFor builds a server with handler h and a client carrying explicit
// tenant/database so path assertions are predictable.
func newClientFor(t *testing.T, h http.HandlerFunc, tenant, database string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, tenant, database)
}

func TestDeleteWhere(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	err := c.DeleteWhere(context.Background(), "coll-1", map[string]interface{}{"source": "workflows/x.md"})
	if err != nil {
		t.Fatalf("DeleteWhere: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/collections/coll-1/delete") {
		t.Errorf("path = %q, want .../collections/coll-1/delete", gotPath)
	}
	where, _ := gotBody["where"].(map[string]interface{})
	if where["source"] != "workflows/x.md" {
		t.Errorf("where = %v, want source filter", gotBody)
	}

	// Refuses an empty filter — a collection-wide delete must be explicit
	// (drop the collection), never an accidental empty where.
	if err := c.DeleteWhere(context.Background(), "coll-1", nil); err == nil {
		t.Error("empty where must refuse")
	}
}
