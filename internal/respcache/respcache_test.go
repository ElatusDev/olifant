package respcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func baseKey() Key {
	return Key{
		Prompt:       "compress this document",
		System:       "you are a compactor",
		Model:        "qwen3:8b",
		ModelVersion: "sha256-abc123",
		Effort:       "high",
		SchemaJSON:   `{"type":"object"}`,
	}
}

func TestKeySHA_EveryFieldParticipates(t *testing.T) {
	base := baseKey().SHA()
	mutations := map[string]Key{}
	k := baseKey()
	k.Prompt += "x"
	mutations["Prompt"] = k
	k = baseKey()
	k.System += "x"
	mutations["System"] = k
	k = baseKey()
	k.Model = "qwen3:14b"
	mutations["Model"] = k
	k = baseKey()
	k.ModelVersion = "sha256-def456"
	mutations["ModelVersion"] = k
	k = baseKey()
	k.Effort = "low"
	mutations["Effort"] = k
	k = baseKey()
	k.SchemaJSON = `{"type":"array"}`
	mutations["SchemaJSON"] = k

	for field, mutated := range mutations {
		if mutated.SHA() == base {
			t.Errorf("changing %s did not change the key SHA", field)
		}
	}
}

func TestKeySHA_FieldBoundariesUnambiguous(t *testing.T) {
	a := Key{Prompt: "ab", System: "c"}
	b := Key{Prompt: "a", System: "bc"}
	if a.SHA() == b.SHA() {
		t.Fatal("field-boundary shift produced a colliding SHA — serialization is not length-prefixed")
	}
}

func TestStore_PutGetRoundtrip(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	payload := json.RawMessage(`{"raw":"the answer","eval_tokens":42}`)
	if err := s.Put(k, Entry{Payload: payload, CacheReadTokens: 2048}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	e, ok := s.Get(k)
	if !ok {
		t.Fatal("Get after Put: miss, want hit")
	}
	if string(e.Payload) != string(payload) {
		t.Errorf("payload = %s, want %s", e.Payload, payload)
	}
	if e.Model != k.Model || e.ModelVersion != k.ModelVersion {
		t.Errorf("entry model = %s/%s, want backfilled from key %s/%s", e.Model, e.ModelVersion, k.Model, k.ModelVersion)
	}
	if e.CacheReadTokens != 2048 {
		t.Errorf("CacheReadTokens = %d, want 2048", e.CacheReadTokens)
	}
	if e.CreatedUnix == 0 {
		t.Error("CreatedUnix not stamped on Put")
	}
}

func TestStore_MissOnAnyKeyFieldChange(t *testing.T) {
	s := testStore(t)
	if err := s.Put(baseKey(), Entry{Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	bumped := baseKey()
	bumped.ModelVersion = "sha256-NEW"
	if _, ok := s.Get(bumped); ok {
		t.Fatal("model-version bump served a stale entry — D-3 bust broken")
	}
}

func TestStore_ObjectLayoutAndAtomicity(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	if err := s.Put(k, Entry{Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sha := k.SHA()
	want := filepath.Join(s.Root(), "objects", sha[:2], sha+".json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("object not at sharded path %s: %v", want, err)
	}
	entries, err := os.ReadDir(filepath.Dir(want))
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".tmp-") {
			t.Errorf("temp file %s left behind — rename not atomic/cleaned", de.Name())
		}
	}
}

func TestStore_LedgerAccumulates(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	s.Get(k)                                                                                        // miss
	if err := s.Put(k, Entry{Payload: json.RawMessage(`{}`), CacheCreationTokens: 7}); err != nil { // store
		t.Fatalf("Put: %v", err)
	}
	s.Get(k) // hit

	raw, err := os.ReadFile(filepath.Join(s.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("ledger lines = %d, want 3 (miss, store, hit): %s", len(lines), raw)
	}
	wantEvents := []string{"miss", "store", "hit"}
	for i, line := range lines {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("ledger line %d not JSON: %v", i, err)
		}
		if r.Event != wantEvents[i] {
			t.Errorf("ledger[%d].event = %q, want %q", i, r.Event, wantEvents[i])
		}
		if r.KeySHA != k.SHA() {
			t.Errorf("ledger[%d].key_sha = %q, want %q", i, r.KeySHA, k.SHA())
		}
		if r.TS == "" {
			t.Errorf("ledger[%d] missing ts", i)
		}
	}
	var storeRec Record
	if err := json.Unmarshal([]byte(lines[1]), &storeRec); err != nil {
		t.Fatal(err)
	}
	if storeRec.CacheCreationTokens != 7 {
		t.Errorf("store record cache_creation_tokens = %d, want 7 (Anthropic pass-through)", storeRec.CacheCreationTokens)
	}
}

func TestDefaultRoot_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	got, err := DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if got != dir {
		t.Errorf("DefaultRoot = %q, want env override %q", got, dir)
	}
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if s.Root() != dir {
		t.Errorf("Open(\"\").Root() = %q, want %q", s.Root(), dir)
	}
}

func TestDefaultRoot_HomeFallback(t *testing.T) {
	t.Setenv(EnvDir, "")
	got, err := DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".olifant", "responses")
	if got != want {
		t.Errorf("DefaultRoot = %q, want %q", got, want)
	}
}

func TestStore_OpenDoesNotMutateFilesystem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	if _, err := Open(root); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("Open created the root directory — opening must be read-only")
	}
}

func TestStore_ConcurrentSameKeyPutIdempotent(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	payload := json.RawMessage(`{"raw":"identical bytes"}`)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(k, Entry{Payload: payload, CreatedUnix: 1}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Put: %v", err)
	}
	e, ok := s.Get(k)
	if !ok {
		t.Fatal("Get after concurrent Puts: miss")
	}
	if string(e.Payload) != string(payload) {
		t.Errorf("payload corrupted under concurrency: %s", e.Payload)
	}
	var parsed Entry
	raw, err := os.ReadFile(filepath.Join(s.Root(), "objects", k.SHA()[:2], k.SHA()+".json"))
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("object not valid JSON after concurrent writes: %v", err)
	}
}

func TestStore_DeleteRemovesAndLedgersOnlyRealEntries(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	s.Delete(k) // absent — must NOT ledger a phantom invalidation
	if err := s.Put(k, Entry{Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.Delete(k)
	if _, ok := s.Get(k); ok {
		t.Fatal("Get after Delete: hit, want miss")
	}
	raw, err := os.ReadFile(filepath.Join(s.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if got := strings.Count(string(raw), `"event":"invalidate"`); got != 1 {
		t.Errorf("invalidate events = %d, want exactly 1 (no phantom for the absent-key Delete)", got)
	}
}

func TestStore_RecordDriftAppends(t *testing.T) {
	s := testStore(t)
	s.RecordDrift(baseKey())
	raw, err := os.ReadFile(filepath.Join(s.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"event":"drift"`) {
		t.Errorf("drift event not ledgered: %s", raw)
	}
}

func TestStore_CorruptObjectTreatedAsMiss(t *testing.T) {
	s := testStore(t)
	k := baseKey()
	sha := k.SHA()
	dir := filepath.Join(s.Root(), "objects", sha[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sha+".json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(k); ok {
		t.Fatal("corrupt object served as a hit — must degrade to miss")
	}
}
