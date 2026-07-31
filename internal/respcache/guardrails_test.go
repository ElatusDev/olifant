package respcache

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// AC3 structural pin (workflow IA5/D-4): the eval-gate lane must not be able
// to reach the response cache. The gate runs challenge.Run / validate.Run
// over synthlib directly — NOT through the PSP executor seam — so the bypass
// is absence of wiring. This import scan keeps that true: the day one of
// these packages imports respcache (or the psp seam that wraps it), the
// bypass claim is void and this test forces the discussion.
func TestEvalLaneNeverImportsResponseCache(t *testing.T) {
	banned := map[string]string{
		"github.com/ElatusDev/olifant/internal/respcache": "the response cache itself",
		"github.com/ElatusDev/olifant/internal/psp":       "the executor seam the cache wraps",
	}
	for _, lane := range []string{"eval", "challenge", "validate", "synth"} {
		dir := filepath.Join("..", lane)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		fset := token.NewFileSet()
		for _, de := range entries {
			name := de.Name()
			if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			fname := filepath.Join(dir, name)
			f, perr := parser.ParseFile(fset, fname, nil, parser.ImportsOnly)
			if perr != nil {
				t.Fatalf("parse %s: %v", fname, perr)
			}
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if why, bad := banned[path]; bad {
					t.Errorf("%s imports %s (%s) — eval-gate bypass-by-construction broken (AC3)", fname, path, why)
				}
			}
		}
	}
}

// Mixed concurrent traffic: readers and writers on overlapping keys must be
// race-clean (ledger mutex + temp/rename objects) and never corrupt entries.
func TestStore_ConcurrentMixedTraffic(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		key := baseKey()
		key.Prompt = fmt.Sprintf("prompt-%d", i%3) // overlap across goroutines
		payload := json.RawMessage(fmt.Sprintf(`{"raw":"resp-%d"}`, i%3))
		wg.Add(2)
		go func(k Key, p json.RawMessage) {
			defer wg.Done()
			if err := s.Put(k, Entry{Payload: p, CreatedUnix: 1}); err != nil {
				t.Errorf("Put: %v", err)
			}
		}(key, payload)
		go func(k Key) {
			defer wg.Done()
			if e, ok := s.Get(k); ok {
				var decoded map[string]string
				if err := json.Unmarshal(e.Payload, &decoded); err != nil {
					t.Errorf("hit served corrupt payload: %v", err)
				}
			}
		}(key)
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(s.Root(), "log.ndjson"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("ledger line %d interleaved/corrupt under concurrency: %v", i, err)
		}
	}
}
