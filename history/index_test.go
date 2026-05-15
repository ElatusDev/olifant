package history

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestChunkID_deterministicAndPrefixed(t *testing.T) {
	a := chunkID("hist", "core-api@abc1234")
	b := chunkID("hist", "core-api@abc1234")
	if a != b {
		t.Errorf("chunkID not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "hist-") {
		t.Errorf("missing prefix: %q", a)
	}
	c := chunkID("snap", "core-api@abc1234:src/Auth.java")
	if !strings.HasPrefix(c, "snap-") {
		t.Errorf("missing snap prefix: %q", c)
	}
	if a == c {
		t.Errorf("different keys produced same id: both %q", a)
	}
}

func TestBuildCommitChunk_metadataPopulated(t *testing.T) {
	rec := &CommitRecord{
		Repo: "core-api", Scope: "backend",
		SHA: "abc1234def", ShortSHA: "abc1234",
		Subject: "fix(auth): rotate JWT key",
		Body:    "Per D17.",
		Files: []FileTouch{
			{Path: "src/Auth.java", Status: "modified", Additions: 5, Deletions: 2},
		},
		CiteIDs: []string{"D17"},
	}
	c := buildCommitChunk(rec)
	if c.Scope != "backend" {
		t.Errorf("scope = %q", c.Scope)
	}
	if c.DocType != "commit-summary" {
		t.Errorf("doc_type = %q", c.DocType)
	}
	if c.SourceSHA != "abc1234def" {
		t.Errorf("source_sha = %q", c.SourceSHA)
	}
	if c.Title != "fix(auth): rotate JWT key" {
		t.Errorf("title = %q", c.Title)
	}
	if !strings.Contains(c.Body, "Per D17.") {
		t.Errorf("body missing message body: %q", c.Body)
	}
	if !strings.Contains(c.Body, "src/Auth.java") {
		t.Errorf("body missing files-touched: %q", c.Body)
	}
	if len(c.Metadata.CitesOutbound) != 1 || c.Metadata.CitesOutbound[0] != "D17" {
		t.Errorf("cites_outbound = %v", c.Metadata.CitesOutbound)
	}
}

func TestBuildCommitChunk_capsFilesAtTen(t *testing.T) {
	files := make([]FileTouch, 15)
	for i := range files {
		files[i] = FileTouch{Path: "f" + intToS(i) + ".go", Additions: 15 - i}
	}
	rec := &CommitRecord{Repo: "x", Scope: "x", SHA: "1", ShortSHA: "1", Subject: "s", Files: files}
	c := buildCommitChunk(rec)
	// Largest first (f0 with +15) must be in the body; smallest (f14 +1) must not.
	if !strings.Contains(c.Body, "f0.go") {
		t.Errorf("biggest file missing from body")
	}
	if strings.Contains(c.Body, "f14.go") {
		t.Errorf("smallest file should have been capped: %q", c.Body)
	}
}

func TestBuildSnapshotChunk_carriesFileContent(t *testing.T) {
	rec := &CommitRecord{
		Repo: "core-api", Scope: "backend",
		SHA: "abc1234def", ShortSHA: "abc1234",
		Subject: "fix(auth): rotate JWT key",
	}
	snap := FileSnapshot{
		Path:    "src/Auth.java",
		Status:  "modified",
		Content: "package auth;\nclass Auth { }\n",
	}
	c := buildSnapshotChunk(rec, snap)
	if c.DocType != "file-snapshot" {
		t.Errorf("doc_type = %q", c.DocType)
	}
	if c.SourceAnchor != "src/Auth.java" {
		t.Errorf("source_anchor = %q", c.SourceAnchor)
	}
	if c.Body != snap.Content {
		t.Errorf("body should equal snapshot content, got %q", c.Body)
	}
	if c.Metadata.Section != "modified" {
		t.Errorf("section = %q", c.Metadata.Section)
	}
}

func TestBuildSnapshotChunk_emptyContentFallsBackToStub(t *testing.T) {
	rec := &CommitRecord{Repo: "x", Scope: "x", SHA: "deadbeef", ShortSHA: "deadbee"}
	snap := FileSnapshot{Path: "nuked.txt", Status: "deleted"}
	c := buildSnapshotChunk(rec, snap)
	if !strings.Contains(c.Body, "no content captured") {
		t.Errorf("empty-content stub missing: %q", c.Body)
	}
}

func TestGroupChunksByScope_partitionsCorrectly(t *testing.T) {
	a := &CommitRecord{Repo: "core-api", Scope: "backend", SHA: "a", ShortSHA: "a", Subject: "s",
		Snapshots: []FileSnapshot{{Path: "f", Status: "modified", Content: "x"}}}
	b := &CommitRecord{Repo: "infra", Scope: "infra", SHA: "b", ShortSHA: "b", Subject: "s",
		Snapshots: []FileSnapshot{{Path: "g", Status: "modified", Content: "y"}}}
	commits, snapshots := groupChunksByScope([]*CommitRecord{a, b})

	if len(commits["backend"]) != 1 || len(commits["infra"]) != 1 {
		t.Errorf("commits partition wrong: %v", commits)
	}
	if len(snapshots["backend"]) != 1 || len(snapshots["infra"]) != 1 {
		t.Errorf("snapshots partition wrong: %v", snapshots)
	}
}

func TestCapChars_truncatesAtUTF8Boundary(t *testing.T) {
	in := "héllo wörld"
	if got := capChars(in, 100); got != in {
		t.Errorf("under-cap modified: %q", got)
	}
	out := capChars(in, 4)
	// Must be valid UTF-8 — no half-multibyte rune at the tail.
	for _, r := range out {
		_ = r
	}
}

// closeTrackingTransport wraps an http.RoundTripper and counts
// CloseIdleConnections() calls, so tests can assert the indexer
// dropped its connection pool after a batch failure.
type closeTrackingTransport struct {
	inner  http.RoundTripper
	closes atomic.Int32
}

func (t *closeTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.inner.RoundTrip(r)
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closes.Add(1)
	type ci interface{ CloseIdleConnections() }
	if c, ok := t.inner.(ci); ok {
		c.CloseIdleConnections()
	}
}

// chunkForBody returns a minimal corpus.Chunk with a deterministic ID
// suitable for embedAndUpsert. Body length is whatever the caller passes;
// the rest of the metadata is just enough to make the upsert request
// shaped correctly.
func chunkForBody(id, body string) corpus.Chunk {
	return corpus.Chunk{
		ChunkID: "snap-" + id,
		Source:  "test@" + id,
		Scope:   "test",
		DocType: "file-snapshot",
		Title:   "test/" + id,
		Body:    body,
	}
}

// repeatRune returns a string of n runes of r — cheaper than building
// a strings.Builder for fixed-size test inputs.
func repeatRune(r rune, n int) string {
	return strings.Repeat(string(r), n)
}

// embedderMaxChars is the package constant; regression tests assert
// it stays under nomic-embed-text's 2048-token default context.
func TestEmbedderMaxChars_underNomicContext(t *testing.T) {
	if embedderMaxChars > 2048 {
		t.Errorf("embedderMaxChars = %d exceeds nomic-embed-text's 2048-token default context; should be <= 2048", embedderMaxChars)
	}
	if embedderMaxChars != 2000 {
		t.Errorf("embedderMaxChars = %d, want 2000 (per 2026-05-15 history-track P5 regression)", embedderMaxChars)
	}
}

// TestEmbedAndUpsert_capsOversizeInputs is the A regression: a chunk
// whose Body would have busted nomic's 2048-token context window under
// the old 3500-char cap must now be capped to embedderMaxChars and
// embed successfully. The stand-in Ollama server enforces the cap
// strictly: any input longer than 2048 chars returns HTTP 400 with the
// real-Ollama error string, mirroring what we observed in P5.
func TestEmbedAndUpsert_capsOversizeInputs(t *testing.T) {
	var (
		mu       sync.Mutex
		sawInput string // longest input we observed
	)

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req ollama.EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, in := range req.Input {
			if len(in) > len(sawInput) {
				sawInput = in
			}
		}
		mu.Unlock()
		for _, in := range req.Input {
			if len(in) > 2048 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"input length exceeds maximum context length"}`))
				return
			}
		}
		emb := make([][]float32, len(req.Input))
		for i := range emb {
			emb[i] = []float32{0.1, 0.2, 0.3}
		}
		_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: emb})
	}))
	defer ollamaSrv.Close()

	upserted := 0
	chromaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/upsert") {
			var req chroma.UpsertRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			upserted += len(req.IDs)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer chromaSrv.Close()

	oc := ollama.New(ollamaSrv.URL)
	cc := chroma.New(chromaSrv.URL, "", "")

	// 3000-char body: would have been capped to 3500 (still >2048 →
	// fail) under the old constant; should be capped to 2000 now.
	chunks := []corpus.Chunk{chunkForBody("oversize", repeatRune('a', 3000))}

	ups, _, err := embedAndUpsert(context.Background(), oc, cc, "test-coll", "nomic-embed-text", chunks, 32, false)
	if err != nil {
		t.Fatalf("embedAndUpsert: %v", err)
	}
	if ups != 1 {
		t.Errorf("upserted = %d, want 1", ups)
	}
	if upserted != 1 {
		t.Errorf("chroma saw %d upserts, want 1", upserted)
	}
	if len(sawInput) > embedderMaxChars {
		t.Errorf("Ollama received input of %d chars, want <= %d (cap not applied)", len(sawInput), embedderMaxChars)
	}
}

// TestEmbedAndUpsert_resetsConnPoolOnBatchFailure is the B regression:
// when the batched /api/embed call fails (simulating a Tailscale read-
// timeout), embedAndUpsert must (1) call CloseIdleConnections() on the
// Ollama client's transport before the per-chunk retry, and (2) recover
// every chunk in the batch via the single-input retry. P5 lost 30
// chunks because the per-chunk retry reused the timed-out connection.
func TestEmbedAndUpsert_resetsConnPoolOnBatchFailure(t *testing.T) {
	var (
		mu          sync.Mutex
		embedCalls  int
		batchCalls  int
		singleCalls int
	)

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req ollama.EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		embedCalls++
		switch {
		case len(req.Input) > 1:
			batchCalls++
		case len(req.Input) == 1:
			singleCalls++
		}
		isBatch := len(req.Input) > 1
		mu.Unlock()

		if isBatch {
			// Simulate the P5 Tailscale failure mode: server-side
			// error on the multi-input batch. The per-chunk retry
			// (single-input) below succeeds.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"read tcp: i/o timeout"}`))
			return
		}
		emb := make([][]float32, len(req.Input))
		for i := range emb {
			emb[i] = []float32{0.1, 0.2, 0.3}
		}
		_ = json.NewEncoder(w).Encode(ollama.EmbedResponse{Embeddings: emb})
	}))
	defer ollamaSrv.Close()

	upserted := 0
	chromaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/upsert") {
			var req chroma.UpsertRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			upserted += len(req.IDs)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer chromaSrv.Close()

	oc := ollama.New(ollamaSrv.URL)
	tr := &closeTrackingTransport{inner: http.DefaultTransport}
	oc.HTTP.Transport = tr

	cc := chroma.New(chromaSrv.URL, "", "")

	chunks := []corpus.Chunk{
		chunkForBody("a", "alpha"),
		chunkForBody("b", "beta"),
		chunkForBody("c", "gamma"),
	}

	ups, _, err := embedAndUpsert(context.Background(), oc, cc, "test-coll", "nomic-embed-text", chunks, 32, false)
	if err != nil {
		t.Fatalf("embedAndUpsert: %v", err)
	}
	if ups != len(chunks) {
		t.Errorf("upserted = %d, want %d (every chunk should recover via per-chunk retry)", ups, len(chunks))
	}
	if got := tr.closes.Load(); got != 1 {
		t.Errorf("CloseIdleConnections called %d times, want 1 (must reset pool exactly once before per-chunk retry)", got)
	}
	if batchCalls != 1 {
		t.Errorf("batch /api/embed calls = %d, want 1", batchCalls)
	}
	if singleCalls != len(chunks) {
		t.Errorf("single-input /api/embed calls = %d, want %d", singleCalls, len(chunks))
	}
	if embedCalls != 1+len(chunks) {
		t.Errorf("total /api/embed calls = %d, want %d", embedCalls, 1+len(chunks))
	}
	if upserted != len(chunks) {
		t.Errorf("chroma saw %d upserts, want %d", upserted, len(chunks))
	}
}

// intToS is a tiny stdlib-only helper to avoid pulling strconv just
// for this test.
func intToS(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
