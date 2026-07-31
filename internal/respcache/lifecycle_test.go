package respcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHitRate_TableDriven(t *testing.T) {
	cases := []struct {
		name                 string
		hits, drifts, misses int
		want                 float64
	}{
		{"zero traffic", 0, 0, 0, 0},
		{"all hits", 4, 0, 0, 1},
		{"all misses", 0, 0, 5, 0},
		{"half", 2, 0, 2, 0.5},
		{"drift moves hit to miss side", 2, 1, 1, float64(1) / 3}, // 1 truly served of 3 lookups (drift is a failed hit, not a 4th lookup)
		{"all hits drifted", 2, 2, 0, 0},
		{"drifts exceed hits (torn ledger) clamps", 1, 3, 0, 0},
	}
	for _, c := range cases {
		if got := HitRate(c.hits, c.drifts, c.misses); got != c.want {
			t.Errorf("%s: HitRate(%d,%d,%d) = %v, want %v", c.name, c.hits, c.drifts, c.misses, got, c.want)
		}
	}
}

// seedStore populates a store with n entries via the public API.
func seedStore(t *testing.T, s *Store, n int) []Key {
	t.Helper()
	keys := make([]Key, 0, n)
	for i := 0; i < n; i++ {
		k := baseKey()
		k.Prompt = "prompt-" + string(rune('a'+i))
		if err := s.Put(k, Entry{Payload: json.RawMessage(`{"raw":"x"}`), CacheCreationTokens: 10, CacheReadTokens: 100}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		keys = append(keys, k)
	}
	return keys
}

func TestStats_CountsEventsTokensAndBytes(t *testing.T) {
	s := testStore(t)
	keys := seedStore(t, s, 3) // 3 stores (+3 miss-free Puts — Put does not Get)
	s.Get(keys[0])             // hit
	s.Get(keys[1])             // hit
	miss := baseKey()
	miss.Prompt = "never-stored"
	s.Get(miss)            // miss
	s.RecordDrift(keys[1]) // one hit was actually a drift
	s.Delete(keys[2])      // invalidate

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Entries != 2 || st.Bytes <= 0 {
		t.Errorf("entries/bytes = %d/%d, want 2 entries after one Delete, bytes>0", st.Entries, st.Bytes)
	}
	if st.Hits != 2 || st.Misses != 1 || st.Stores != 3 || st.Invalidates != 1 || st.Drifts != 1 {
		t.Errorf("event counts = %+v, want hits2 miss1 store3 inval1 drift1", st)
	}
	if st.CacheCreationTokens != 30 || st.CacheReadTokens != 300 {
		t.Errorf("token sums = %d/%d, want 30/300 (from store records)", st.CacheCreationTokens, st.CacheReadTokens)
	}
	// served = 2-1 = 1 of (1 + 1 miss + 1 drift) = 33.3…%
	if st.HitRatePct < 33.3 || st.HitRatePct > 33.4 {
		t.Errorf("HitRatePct = %v, want ~33.3 (D-2 drift subtraction)", st.HitRatePct)
	}
}

func TestStats_EmptyStore(t *testing.T) {
	s := testStore(t)
	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats on empty store: %v", err)
	}
	if st.Entries != 0 || st.HitRatePct != 0 {
		t.Errorf("empty store stats = %+v, want zeros", st)
	}
}

func TestPrune_RefusesUnbounded(t *testing.T) {
	s := testStore(t)
	if _, err := s.Prune(PruneOptions{}); err == nil {
		t.Fatal("unbounded prune must be refused")
	}
}

func TestPrune_AgeBound(t *testing.T) {
	s := testStore(t)
	keys := seedStore(t, s, 3)
	old := filepath.Join(s.Root(), "objects", keys[0].SHA()[:2], keys[0].SHA()+".json")
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(PruneOptions{OlderThan: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Deleted != 1 || res.Remaining != 2 {
		t.Errorf("deleted/remaining = %d/%d, want 1/2", res.Deleted, res.Remaining)
	}
	if _, ok := s.Get(keys[0]); ok {
		t.Error("aged entry still served after prune")
	}
	if _, ok := s.Get(keys[1]); !ok {
		t.Error("young entry deleted by age-bounded prune")
	}
	raw, _ := os.ReadFile(filepath.Join(s.Root(), "log.ndjson"))
	if !strings.Contains(string(raw), `"event":"prune"`) {
		t.Error("prune summary record not ledgered")
	}
}

func TestPrune_SizeBoundEvictsOldestFirst(t *testing.T) {
	s := testStore(t)
	keys := seedStore(t, s, 3)
	// Make key[0] the oldest, key[2] the newest.
	for i, k := range keys {
		p := filepath.Join(s.Root(), "objects", k.SHA()[:2], k.SHA()+".json")
		mt := time.Now().Add(-time.Duration(len(keys)-i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Stats()
	perEntry := st.Bytes / 3
	res, err := s.Prune(PruneOptions{MaxBytes: perEntry * 2}) // room for 2
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (evict to fit)", res.Deleted)
	}
	if _, ok := s.Get(keys[0]); ok {
		t.Error("oldest entry survived a size-bound prune — eviction order broken")
	}
	if _, ok := s.Get(keys[2]); !ok {
		t.Error("newest entry evicted — eviction order broken")
	}
}

func TestPrune_DryRunParity(t *testing.T) {
	s := testStore(t)
	seedStore(t, s, 3)
	dry, err := s.Prune(PruneOptions{All: true, DryRun: true})
	if err != nil {
		t.Fatalf("dry prune: %v", err)
	}
	st, _ := s.Stats()
	if st.Entries != 3 {
		t.Fatalf("dry-run deleted entries: %d left, want 3", st.Entries)
	}
	real, err := s.Prune(PruneOptions{All: true})
	if err != nil {
		t.Fatalf("real prune: %v", err)
	}
	if dry.Deleted != real.Deleted || dry.ReclaimedBytes != real.ReclaimedBytes {
		t.Errorf("dry(%+v) != real(%+v) — parity broken", dry, real)
	}
	if st2, _ := s.Stats(); st2.Entries != 0 {
		t.Errorf("entries after All prune = %d, want 0", st2.Entries)
	}
}

func TestPrune_TempReapGuardsFreshWrites(t *testing.T) {
	s := testStore(t)
	keys := seedStore(t, s, 1)
	shard := filepath.Join(s.Root(), "objects", keys[0].SHA()[:2])
	oldTmp := filepath.Join(shard, ".tmp-old-123")
	freshTmp := filepath.Join(shard, ".tmp-fresh-456")
	for _, p := range []string{oldTmp, freshTmp} {
		if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldTmp, stale, stale); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(PruneOptions{OlderThan: 240 * time.Hour}) // age bound that dooms no entries
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.TempReaped != 1 {
		t.Errorf("TempReaped = %d, want 1 (only the >1h orphan)", res.TempReaped)
	}
	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Error("stale tmp not removed")
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Error("fresh tmp removed — in-flight write unprotected")
	}
}

func TestPrune_DryRunWordsFutureTense(t *testing.T) {
	// The count parity is tested above; this pins that a dry run reaps no
	// temp files either (the CLI's "would reap" claim).
	s := testStore(t)
	seedStore(t, s, 1)
	shardParent := filepath.Join(s.Root(), "objects")
	shards, err := os.ReadDir(shardParent)
	if err != nil || len(shards) == 0 {
		t.Fatalf("no shards: %v", err)
	}
	tmp := filepath.Join(shardParent, shards[0].Name(), ".tmp-dry-1")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(tmp, stale, stale); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(PruneOptions{All: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.TempReaped != 1 {
		t.Errorf("dry-run TempReaped = %d, want 1 (reported)", res.TempReaped)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Error("dry-run removed the temp file")
	}
}
