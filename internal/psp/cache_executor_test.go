package psp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElatusDev/olifant/internal/respcache"
)

// countingExecutor is a fake backend that counts invocations and exposes
// cache key material.
type countingExecutor struct {
	calls int
	resp  Response
}

func (f *countingExecutor) ID() string { return "fake-model" }

func (f *countingExecutor) Execute(_ context.Context, _ string, _ map[string]interface{}) (Response, error) {
	f.calls++
	return f.resp, nil
}

func (f *countingExecutor) CacheKeyBase() respcache.Key {
	return respcache.Key{System: "sys", Model: "fake-model", Effort: "high"}
}

// keylessExecutor implements Executor but not CacheKeyer.
type keylessExecutor struct{}

func (keylessExecutor) ID() string { return "keyless" }
func (keylessExecutor) Execute(_ context.Context, _ string, _ map[string]interface{}) (Response, error) {
	return Response{}, nil
}

func testCacheStore(t *testing.T) *respcache.Store {
	t.Helper()
	s, err := respcache.Open(t.TempDir())
	if err != nil {
		t.Fatalf("respcache.Open: %v", err)
	}
	return s
}

func TestCacheExecutor_WarmHitElidesInnerCall(t *testing.T) {
	inner := &countingExecutor{resp: Response{RawText: "answer", EvalTokens: 5, CacheReadTokens: 2048}}
	ce := NewCacheExecutor(inner, testCacheStore(t), false)

	first, err := ce.Execute(context.Background(), "prompt-1", map[string]interface{}{"type": "object"})
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := ce.Execute(context.Background(), "prompt-1", map[string]interface{}{"type": "object"})
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 — warm hit must make ZERO backend calls (AC1)", inner.calls)
	}
	if second.RawText != first.RawText || second.EvalTokens != first.EvalTokens || second.CacheReadTokens != first.CacheReadTokens {
		t.Errorf("cached response %+v != original %+v", second, first)
	}
}

func TestCacheExecutor_MissStoresObjectAndLedger(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: "fresh", CacheCreationTokens: 7}}
	ce := NewCacheExecutor(inner, store, false)

	if _, err := ce.Execute(context.Background(), "prompt-2", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	objects := 0
	err := filepath.WalkDir(filepath.Join(store.Root(), "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			objects++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	if objects != 1 {
		t.Errorf("stored objects = %d, want 1 (AC2)", objects)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"event":"store"`) || !strings.Contains(string(raw), `"cache_creation_tokens":7`) {
		t.Errorf("ledger missing store record with pass-through tokens (AC2): %s", raw)
	}
}

func TestCacheExecutor_DifferentPromptOrSchemaMisses(t *testing.T) {
	inner := &countingExecutor{resp: Response{RawText: "x"}}
	ce := NewCacheExecutor(inner, testCacheStore(t), false)
	ctx := context.Background()

	mustExecute(t, ce, ctx, "prompt-A", nil)
	mustExecute(t, ce, ctx, "prompt-B", nil)                                     // prompt differs
	mustExecute(t, ce, ctx, "prompt-A", map[string]interface{}{"type": "array"}) // schema differs
	if inner.calls != 3 {
		t.Errorf("inner calls = %d, want 3 — every key component must participate", inner.calls)
	}
}

func TestCacheExecutor_RefreshBypassesReadStillWrites(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: "v1"}}
	warm := NewCacheExecutor(inner, store, false)
	mustExecute(t, warm, context.Background(), "prompt-r", nil)

	inner.resp = Response{RawText: "v2"}
	refreshing := NewCacheExecutor(inner, store, true)
	got := mustExecute(t, refreshing, context.Background(), "prompt-r", nil)
	if inner.calls != 2 {
		t.Fatalf("inner calls = %d, want 2 — --refresh must re-invoke (AC5)", inner.calls)
	}
	if got.RawText != "v2" {
		t.Errorf("refresh served %q, want fresh %q", got.RawText, "v2")
	}

	rewarmed := NewCacheExecutor(inner, store, false)
	served := mustExecute(t, rewarmed, context.Background(), "prompt-r", nil)
	if inner.calls != 2 {
		t.Errorf("inner calls = %d after re-warm, want 2 — refresh must overwrite the stored entry", inner.calls)
	}
	if served.RawText != "v2" {
		t.Errorf("post-refresh cache serves %q, want overwritten %q", served.RawText, "v2")
	}
}

func TestNewCacheExecutor_KeylessInnerReturnedUnwrapped(t *testing.T) {
	inner := keylessExecutor{}
	got := NewCacheExecutor(inner, testCacheStore(t), false)
	if _, wrapped := got.(*CacheExecutor); wrapped {
		t.Fatal("executor without CacheKeyBase was wrapped — unknown executors must stay uncached, never get a lossy key")
	}
}

func TestCacheExecutor_IDDelegatesToInner(t *testing.T) {
	ce := NewCacheExecutor(&countingExecutor{}, testCacheStore(t), false)
	if ce.ID() != "fake-model" {
		t.Errorf("ID() = %q, want inner's %q", ce.ID(), "fake-model")
	}
}

func TestLocalAndClaudeExecutors_ExposeKeyMaterial(t *testing.T) {
	local := NewLocalExecutor("http://127.0.0.1:1", "qwen3:8b")
	lk := local.CacheKeyBase()
	if lk.Model != "qwen3:8b" {
		t.Errorf("LocalExecutor key model = %q, want %q", lk.Model, "qwen3:8b")
	}
	claude := NewClaudeCodeExecutor("claude", "claude-opus-4-8", "high", 0, "")
	ck := claude.CacheKeyBase()
	if ck.Model != "claude-opus-4-8" || ck.Effort != "high" || ck.System == "" {
		t.Errorf("ClaudeCodeExecutor key = %+v, want model/effort/system populated", ck)
	}
	var buf1, buf2 respcache.Key
	buf1, buf2 = lk, ck
	if buf1.SHA() == buf2.SHA() {
		t.Error("local and claude key bases collide")
	}
}

func mustExecute(t *testing.T, e Executor, ctx context.Context, prompt string, schema map[string]interface{}) Response {
	t.Helper()
	resp, err := e.Execute(ctx, prompt, schema)
	if err != nil {
		t.Fatalf("Execute(%q): %v", prompt, err)
	}
	return resp
}

// The load-bearing runner interplay (review finding prr:nak-cache): a
// response that fails step validation is NAKed by the runner — it must be
// invalidated, or every future run of the plan replays the cached failure
// deterministically and burns its retry budget with zero live model calls.
func TestRun_NAKedResponseInvalidatedNotReplayed(t *testing.T) {
	store := testCacheStore(t)
	// Output nil against a schema-carrying step ⇒ blocker ⇒ NAK.
	failing := &countingExecutor{resp: Response{RawText: "unparseable"}}
	failing.resp.Output = nil
	wrapped := NewCacheExecutor(failing, store, false)

	step := minimalStep("step_01", "")
	step.RetryPolicy = RetryPolicy{MaxAttempts: 1}
	plan := minimalPlan([]Step{step})
	cfg := RunnerConfig{Executor: wrapped, Executors: map[string]Executor{ExecutorKindLocal: wrapped}, Plan: plan}

	res1, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run 1: unexpected hard error: %v", err)
	}
	if res1.State != StateClosedError {
		t.Fatalf("run 1 state = %v, want ClosedError (terminal NAK)", res1.State)
	}
	if failing.calls != 1 {
		t.Fatalf("run 1 live calls = %d, want 1", failing.calls)
	}
	res2, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run 2: unexpected hard error: %v", err)
	}
	if res2.State != StateClosedError {
		t.Fatalf("run 2 state = %v, want ClosedError", res2.State)
	}
	if failing.calls != 2 {
		t.Errorf("run 2 live calls = %d total, want 2 — NAKed response was served from cache (retry budget burns on replayed failures)", failing.calls)
	}
}

func TestCacheExecutor_InvalidateDropsEntry(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: "v", Output: StepOutput{"ok": true}}}
	ce := NewCacheExecutor(inner, store, false).(*CacheExecutor)
	ctx := context.Background()

	mustExecute(t, ce, ctx, "prompt-inv", nil)
	ce.Invalidate("prompt-inv", nil)
	mustExecute(t, ce, ctx, "prompt-inv", nil)
	if inner.calls != 2 {
		t.Errorf("inner calls = %d, want 2 — Invalidate must drop the stored entry", inner.calls)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"event":"invalidate"`) {
		t.Errorf("ledger missing invalidate event: %s", raw)
	}
}

// Guard against payload-format drift: a stored entry whose payload no longer
// decodes as a Response must fall through to the live backend, not error.
func TestCacheExecutor_UndecodablePayloadFallsThrough(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: "live"}}
	ce := NewCacheExecutor(inner, store, false).(*CacheExecutor)

	key := ce.keyBase
	key.Prompt = "prompt-drift"
	if err := store.Put(key, respcache.Entry{Payload: json.RawMessage(`"not a response object"`)}); err != nil {
		t.Fatalf("seed corrupt payload: %v", err)
	}
	got := mustExecute(t, ce, context.Background(), "prompt-drift", nil)
	if inner.calls != 1 || got.RawText != "live" {
		t.Errorf("calls=%d raw=%q — undecodable payload must fall through to live call", inner.calls, got.RawText)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"event":"drift"`) {
		t.Errorf("fallthrough not ledgered as drift — S2 hit-rate would overcount: %s", raw)
	}
}

// #121 A3: the served-from-cache flag is set per-serving, never persisted,
// and plumbed runner → StepResult → StepSummary (AC3).
func TestRun_ServedFromCachePlumbedToStepResult(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: `{"ok":true}`, Output: StepOutput{"ok": true}}}
	wrapped := NewCacheExecutor(inner, store, false)
	step := minimalStep("step_01", "")
	plan := minimalPlan([]Step{step})
	cfg := RunnerConfig{Executor: wrapped, Executors: map[string]Executor{ExecutorKindLocal: wrapped}, Plan: plan}

	cold, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("cold run: %v", err)
	}
	if cold.Steps[0].ServedFromCache {
		t.Error("cold run marked served-from-cache")
	}
	warm, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("warm run: %v", err)
	}
	if !warm.Steps[0].ServedFromCache {
		t.Error("warm run not marked served-from-cache (AC3)")
	}
	if inner.calls != 1 {
		t.Errorf("inner calls = %d, want 1", inner.calls)
	}
	var found bool
	for _, ss := range warm.Aggregate.StepSummaries {
		if ss.ServedFromCache {
			found = true
		}
	}
	if !found {
		t.Error("aggregate StepSummary missing served_from_cache")
	}
}

// Stored payloads must always carry ServedFromCache=false (D-4): the flag is
// set on the served copy after unmarshal, so a hit-of-a-hit stays true only
// per-serving and the object bytes stay stable.
func TestCacheExecutor_StoredPayloadNeverMarkedServed(t *testing.T) {
	store := testCacheStore(t)
	inner := &countingExecutor{resp: Response{RawText: "v", Output: StepOutput{"ok": true}}}
	ce := NewCacheExecutor(inner, store, false).(*CacheExecutor)
	ctx := context.Background()
	mustExecute(t, ce, ctx, "prompt-flag", nil)

	key := ce.key("prompt-flag", nil)
	entry, ok := store.Get(key)
	if !ok {
		t.Fatal("entry missing after miss+store")
	}
	var stored Response
	if err := json.Unmarshal(entry.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ServedFromCache {
		t.Error("stored payload marked served — D-4 violated, object bytes not stable")
	}
	served := mustExecute(t, ce, ctx, "prompt-flag", nil)
	if !served.ServedFromCache {
		t.Error("served copy not flagged")
	}
}
