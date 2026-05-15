// Package ollama is a thin Go client for the local Ollama HTTP API
// (https://github.com/ollama/ollama/blob/main/docs/api.md). Only the surface
// olifant uses: /api/version, /api/embed (batched), /api/generate.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is safe for concurrent use.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client with a generous default timeout (large models can
// take >60 s on a long synthesizer prompt).
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// CloseIdle drops all idle keep-alive connections from the underlying
// HTTP transport's pool. Call after a request failure suspected to be
// caused by a stale connection (read-timeout mid-response, EOF, etc.)
// so the next request reaches the server over a fresh TCP connection
// instead of reusing the half-dead one. Cheap; safe to call always.
func (c *Client) CloseIdle() {
	c.HTTP.CloseIdleConnections()
}

// Version returns the Ollama server version (smoke-test).
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/version", nil, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// EmbedRequest matches the /api/embed body.
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// Truncate=true silently caps inputs to the model's context window.
	// Without this, an oversize input aborts the whole batch with HTTP 400.
	Truncate bool `json:"truncate"`
}

// EmbedResponse — embeddings is one float vector per input element, in order.
type EmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float32 `json:"embeddings"`
	TotalDuration   int64       `json:"total_duration"`
	LoadDuration    int64       `json:"load_duration"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embed returns one embedding per input string, in the same order.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	req := EmbedRequest{Model: model, Input: inputs, Truncate: true}
	var out EmbedResponse
	if err := c.do(ctx, http.MethodPost, "/api/embed", req, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama: requested %d embeddings, received %d", len(inputs), len(out.Embeddings))
	}
	return out.Embeddings, nil
}

// GenerateRequest matches the /api/generate body for non-streamed calls.
//
// Format can be:
//   - nil / omitted   — free-form output
//   - "json"          — model must emit any valid JSON
//   - map[string]any  — a JSON Schema; model is constrained to emit conformant
//                       output via grammar-restricted decoding (Ollama ≥ 0.5).
type GenerateRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	System  string                 `json:"system,omitempty"`
	Stream  bool                   `json:"stream"` // always false here
	Options map[string]interface{} `json:"options,omitempty"`
	Format  interface{}            `json:"format,omitempty"`
	Suffix  string                 `json:"suffix,omitempty"`
}

// GenerateResponse — the synthesized text plus timing metadata.
type GenerateResponse struct {
	Model              string `json:"model"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
}

// Generate runs one non-streamed completion against the given model.
func (c *Client) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req.Stream = false
	var out GenerateResponse
	if err := c.do(ctx, http.MethodPost, "/api/generate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TokensPerSec is a convenience metric for benchmarking.
func (r *GenerateResponse) TokensPerSec() float64 {
	if r.EvalDuration <= 0 || r.EvalCount <= 0 {
		return 0
	}
	return float64(r.EvalCount) / (float64(r.EvalDuration) / 1e9)
}

// do is the single HTTP helper used by every method.
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
		return fmt.Errorf("ollama %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
