package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/respcache"
)

// captureStdout runs f and returns what it printed to stdout.
func captureStdout(t *testing.T, f func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := f()
	os.Stdout = old
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// seedCacheDir points OLIFANT_RESPONSE_CACHE_DIR at a temp store with n
// entries and some ledger traffic; returns the store.
func seedCacheDir(t *testing.T, n int) *respcache.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(respcache.EnvDir, dir)
	s, err := respcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		k := respcache.Key{Prompt: "p" + string(rune('a'+i)), Model: "m"}
		if err := s.Put(k, respcache.Entry{Payload: json.RawMessage(`{"raw":"x"}`), CacheReadTokens: 50}); err != nil {
			t.Fatal(err)
		}
		s.Get(k) // one hit each
	}
	return s
}

func TestCache_UsageAndUnknown(t *testing.T) {
	if code := Cache(nil); code != 2 {
		t.Errorf("no-args exit = %d, want 2", code)
	}
	if code := Cache([]string{"bogus"}); code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}
}

func TestCacheStatus_HumanAndJSON(t *testing.T) {
	seedCacheDir(t, 2)
	out, code := captureStdout(t, func() int { return Cache([]string{"status"}) })
	if code != 0 {
		t.Fatalf("status exit = %d\n%s", code, out)
	}
	for _, want := range []string{"entries: 2", "hits=2", "stores=2", "hit-rate: 100.0%", "cache_read=100"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	jout, code := captureStdout(t, func() int { return Cache([]string{"status", "-json"}) })
	if code != 0 {
		t.Fatalf("status -json exit = %d", code)
	}
	var st respcache.Stats
	if err := json.Unmarshal([]byte(jout), &st); err != nil {
		t.Fatalf("status -json not parseable: %v\n%s", err, jout)
	}
	if st.Entries != 2 || st.Hits != 2 || st.CacheReadTokens != 100 {
		t.Errorf("json stats = %+v, want entries2 hits2 read100", st)
	}
}

func TestCachePrune_RefusesUnboundedAndDryRuns(t *testing.T) {
	seedCacheDir(t, 2)
	if _, code := captureStdout(t, func() int { return Cache([]string{"prune"}) }); code != 1 {
		t.Errorf("unbounded prune exit = %d, want 1 (store refusal)", code)
	}
	out, code := captureStdout(t, func() int { return Cache([]string{"prune", "--all", "--dry-run"}) })
	if code != 0 || !strings.Contains(out, "would delete 2 entries") {
		t.Errorf("dry-run exit=%d out=%q", code, out)
	}
	s, _ := respcache.Open("")
	if st, _ := s.Stats(); st.Entries != 2 {
		t.Errorf("dry-run deleted entries: %d left, want 2", st.Entries)
	}
	out, code = captureStdout(t, func() int { return Cache([]string{"prune", "--all"}) })
	if code != 0 || !strings.Contains(out, "deleted 2 entries") {
		t.Errorf("real prune exit=%d out=%q", code, out)
	}
	if st, _ := s.Stats(); st.Entries != 0 {
		t.Errorf("entries after --all prune = %d, want 0", st.Entries)
	}
}

func TestCachePrune_AgeFlagParsing(t *testing.T) {
	s := seedCacheDir(t, 1)
	if _, code := captureStdout(t, func() int { return Cache([]string{"prune", "--older-than", "not-a-duration"}) }); code != 2 {
		t.Errorf("bad duration exit = %d, want 2", code)
	}
	// Age an entry and prune it via the flag path.
	k := respcache.Key{Prompt: "pa", Model: "m"}
	p := filepath.Join(s.Root(), "objects", k.SHA()[:2], k.SHA()+".json")
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	out, code := captureStdout(t, func() int { return Cache([]string{"prune", "--older-than", "24h"}) })
	if code != 0 || !strings.Contains(out, "deleted 1 entries") {
		t.Errorf("age prune exit=%d out=%q", code, out)
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{"500MB": 500 << 20, "2GB": 2 << 30, "1024KB": 1024 << 10, "12345": 12345, "10B": 10}
	for in, want := range cases {
		got, err := parseBytes(in)
		if err != nil || got != want {
			t.Errorf("parseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "-5MB", "1.5GB"} {
		if _, err := parseBytes(bad); err == nil {
			t.Errorf("parseBytes(%q) should error", bad)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{10: "10B", 2048: "2.0KB", 3 << 20: "3.0MB", 5 << 30: "5.0GB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBytes_OverflowRejected(t *testing.T) {
	if _, err := parseBytes("9999999999GB"); err == nil {
		t.Error("overflowing size should error, not wrap")
	}
}
