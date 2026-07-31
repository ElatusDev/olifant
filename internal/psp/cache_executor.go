// CacheExecutor — response-cache middleware at the executor seam (epic #119
// S1). It decorates any Executor whose construction-time key material is
// known, serving exact-key hits from the on-disk respcache store with zero
// model calls and storing misses on the way out. The eval-gate lane never
// constructs this wrapper (it runs synthlib directly, not the PSP runner),
// so gate receipts always exercise the live model — the bypass is absence
// of wiring, not a flag (workflow D-4 / AP285 class).
package psp

import (
	"context"
	"encoding/json"

	"github.com/ElatusDev/olifant/internal/respcache"
)

// CacheKeyer is implemented by executors that can expose the
// construction-time fields of their cache key (system prompt, model,
// model version, effort). Prompt and schema are per-call and are filled
// in by the CacheExecutor itself.
type CacheKeyer interface {
	CacheKeyBase() respcache.Key
}

// CacheKeyBase exposes the LocalExecutor's key material. Ollama has no
// separate system block (the prompt is the full text) and the model tag is
// the only version signal available without a live /api/show call.
func (e *LocalExecutor) CacheKeyBase() respcache.Key {
	return respcache.Key{Model: e.model}
}

// CacheKeyBase exposes the ClaudeCodeExecutor's key material. Claude model
// ids are already version-carrying; the stable system prompt participates
// so a prompt revision dead-ends stale entries.
func (e *ClaudeCodeExecutor) CacheKeyBase() respcache.Key {
	return respcache.Key{System: e.systemPrompt, Model: e.model, Effort: e.effort}
}

// CacheExecutor wraps an inner Executor with the response cache.
type CacheExecutor struct {
	inner   Executor
	store   *respcache.Store
	keyBase respcache.Key
	refresh bool // skip the read path, still write — `--refresh`
}

// NewCacheExecutor decorates inner with the response cache. An inner
// executor that does not implement CacheKeyer is returned unwrapped —
// unknown executors default to uncached rather than to a lossy key.
func NewCacheExecutor(inner Executor, store *respcache.Store, refresh bool) Executor {
	keyer, ok := inner.(CacheKeyer)
	if !ok {
		return inner
	}
	return &CacheExecutor{inner: inner, store: store, keyBase: keyer.CacheKeyBase(), refresh: refresh}
}

// ID delegates to the inner executor so SYN_ACK logging and the aggregate
// report the real backend.
func (c *CacheExecutor) ID() string { return c.inner.ID() }

// Execute serves an exact-key hit from the store (zero inner calls) or
// delegates and stores the result. Store failures never fail the call —
// the cache is an optimization, the model response is the product.
func (c *CacheExecutor) Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error) {
	key := c.keyBase
	key.Prompt = prompt
	if len(schema) > 0 {
		// json.Marshal sorts map keys — canonical enough for exact-match.
		if raw, err := json.Marshal(schema); err == nil {
			key.SchemaJSON = string(raw)
		}
	}
	if !c.refresh {
		if entry, ok := c.store.Get(key); ok {
			var resp Response
			if err := json.Unmarshal(entry.Payload, &resp); err == nil {
				return resp, nil
			}
			// Undecodable payload (format drift): fall through and overwrite.
		}
	}
	resp, err := c.inner.Execute(ctx, prompt, schema)
	if err != nil {
		return resp, err
	}
	if payload, merr := json.Marshal(resp); merr == nil {
		_ = c.store.Put(key, respcache.Entry{
			Payload:             payload,
			CacheCreationTokens: resp.CacheCreationTokens,
			CacheReadTokens:     resp.CacheReadTokens,
		})
	}
	return resp, nil
}
