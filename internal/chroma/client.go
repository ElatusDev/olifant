// Package chroma is a minimal Go client for ChromaDB v2 HTTP API
// (chromadb >= 0.6.x). Only the surface olifant uses: heartbeat, ensure
// tenant+database+collection, batched upsert, similarity query.
//
// API reference: https://docs.trychroma.com/ (REST API section).
package chroma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is safe for concurrent use.
type Client struct {
	BaseURL  string
	Tenant   string
	Database string
	HTTP     *http.Client
}

// New constructs a client. tenant + database default to "default_*" if empty.
func New(baseURL, tenant, database string) *Client {
	if tenant == "" {
		tenant = "default_tenant"
	}
	if database == "" {
		database = "default_database"
	}
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Tenant:   tenant,
		Database: database,
		HTTP:     &http.Client{Timeout: 60 * time.Second},
	}
}

// ===== Heartbeat / smoke =====

// Heartbeat hits /api/v2/heartbeat and returns the server's nanosecond stamp.
func (c *Client) Heartbeat(ctx context.Context) (int64, error) {
	var out struct {
		HB int64 `json:"nanosecond heartbeat"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/heartbeat", nil, &out); err != nil {
		return 0, err
	}
	return out.HB, nil
}

// ===== Tenant + Database (idempotent) =====

// EnsureTenant creates the tenant if missing. No-op when already present.
func (c *Client) EnsureTenant(ctx context.Context) error {
	body := map[string]string{"name": c.Tenant}
	err := c.do(ctx, http.MethodPost, "/api/v2/tenants", body, nil)
	if err == nil || isAlreadyExists(err) {
		return nil
	}
	return err
}

// EnsureDatabase creates the database under the tenant if missing.
func (c *Client) EnsureDatabase(ctx context.Context) error {
	body := map[string]string{"name": c.Database}
	path := fmt.Sprintf("/api/v2/tenants/%s/databases", url.PathEscape(c.Tenant))
	err := c.do(ctx, http.MethodPost, path, body, nil)
	if err == nil || isAlreadyExists(err) {
		return nil
	}
	return err
}

// ===== Collections =====

// Collection is the metadata returned by Chroma.
type Collection struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// EnsureCollection creates the collection if missing, then returns its details.
// `metadata` may include the hnsw distance function (we use "cosine") etc.
func (c *Client) EnsureCollection(ctx context.Context, name string, metadata map[string]interface{}) (*Collection, error) {
	if metadata == nil {
		metadata = map[string]interface{}{"hnsw:space": "cosine"}
	}
	body := map[string]interface{}{
		"name":          name,
		"metadata":      metadata,
		"get_or_create": true,
	}
	path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections", url.PathEscape(c.Tenant), url.PathEscape(c.Database))
	var out Collection
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ===== Upsert =====

// UpsertRequest carries parallel slices — ids[i] ↔ embeddings[i] ↔ documents[i] ↔ metadatas[i].
// Slices may be left nil/empty if Chroma should generate them, but for our use
// all four are required.
type UpsertRequest struct {
	IDs        []string                 `json:"ids"`
	Embeddings [][]float32              `json:"embeddings"`
	Documents  []string                 `json:"documents,omitempty"`
	Metadatas  []map[string]interface{} `json:"metadatas,omitempty"`
}

// Count returns the number of chunks currently stored in the named
// collection. Used by `olifant history stats` and similar
// read-only probes that want a row count without pulling any
// content back.
func (c *Client) Count(ctx context.Context, collectionID string) (int64, error) {
	path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s/count",
		url.PathEscape(c.Tenant), url.PathEscape(c.Database), url.PathEscape(collectionID))
	var n int64
	if err := c.do(ctx, http.MethodGet, path, nil, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// Upsert performs an idempotent insert-or-update on the collection.
// Use small batches (≤ 256 vectors) — large batches can exceed HTTP timeouts.
func (c *Client) Upsert(ctx context.Context, collectionID string, req UpsertRequest) error {
	if len(req.IDs) == 0 {
		return nil
	}
	path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s/upsert",
		url.PathEscape(c.Tenant), url.PathEscape(c.Database), url.PathEscape(collectionID))
	return c.do(ctx, http.MethodPost, path, req, nil)
}

// ===== Query =====

// QueryRequest is a similarity query against a collection.
type QueryRequest struct {
	QueryEmbeddings [][]float32            `json:"query_embeddings"`
	NResults        int                    `json:"n_results"`
	Where           map[string]interface{} `json:"where,omitempty"`
	WhereDocument   map[string]interface{} `json:"where_document,omitempty"`
	Include         []string               `json:"include,omitempty"` // e.g., ["documents","metadatas","distances"]
}

// QueryResponse — one inner slice per query (we send 1 query, expect 1 inner slice).
type QueryResponse struct {
	IDs       [][]string                 `json:"ids"`
	Documents [][]string                 `json:"documents"`
	Metadatas [][]map[string]interface{} `json:"metadatas"`
	Distances [][]float32                `json:"distances"`
}

// Query runs a single-query similarity search.
func (c *Client) Query(ctx context.Context, collectionID string, req QueryRequest) (*QueryResponse, error) {
	if len(req.Include) == 0 {
		req.Include = []string{"documents", "metadatas", "distances"}
	}
	path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s/query",
		url.PathEscape(c.Tenant), url.PathEscape(c.Database), url.PathEscape(collectionID))
	var out QueryResponse
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ===== Plumbing =====

func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(buf)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return &httpError{
			Method: method, Path: path,
			Status: resp.StatusCode,
			Body:   strings.TrimSpace(string(errBody)),
		}
	}
	if out == nil {
		// drain
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type httpError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("chroma %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// isAlreadyExists checks for Chroma's 409 / "already exists" responses.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*httpError); ok {
		if he.Status == http.StatusConflict {
			return true
		}
		if strings.Contains(strings.ToLower(he.Body), "already exists") {
			return true
		}
	}
	return false
}
