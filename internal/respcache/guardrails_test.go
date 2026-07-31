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
// is absence of wiring. Two invariants enforce that:
//
//  1. The lane packages and the gate's cmd entrypoints (`cmd/eval*.go`)
//     never DIRECTLY import respcache or psp. (Type-level transitive
//     reachability — eval→advice→prompt→psp for Plan types — is legitimate;
//     the cache only engages where a CacheExecutor is constructed.)
//  2. `NewCacheExecutor` is constructed in exactly the known wiring sites —
//     today only `cmd/run.go`. A new wiring site (e.g. someone routing the
//     gate through cached executors in `cmd/eval.go`) trips this test and
//     forces the bypass discussion.
func TestEvalLaneNeverImportsResponseCache(t *testing.T) {
	const modPrefix = "github.com/ElatusDev/olifant/"
	banned := map[string]string{
		modPrefix + "internal/respcache": "the response cache itself",
		modPrefix + "internal/psp":       "the executor seam the cache wraps",
	}
	repoRoot := filepath.Join("..", "..")
	fset := token.NewFileSet()

	var files []string
	for _, lane := range []string{"eval", "challenge", "validate", "synth"} {
		dir := filepath.Join(repoRoot, "internal", lane)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, de := range entries {
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".go") && !strings.HasSuffix(de.Name(), "_test.go") {
				files = append(files, filepath.Join(dir, de.Name()))
			}
		}
	}
	evalCmds, _ := filepath.Glob(filepath.Join(repoRoot, "cmd", "eval*.go"))
	if len(evalCmds) == 0 {
		t.Fatal("no cmd/eval*.go entrypoints found — scan roots are stale")
	}
	for _, f := range evalCmds {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}
	for _, fname := range files {
		f, perr := parser.ParseFile(fset, fname, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", fname, perr)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[p]; bad {
				t.Errorf("%s imports %s (%s) — eval-gate bypass-by-construction broken (AC3)", fname, p, why)
			}
		}
	}
}

// Invariant 2: the cache wrapper is constructed ONLY at the known wiring
// sites. Scans every non-test .go file in the repo for `NewCacheExecutor(`.
func TestCacheExecutorWiredOnlyAtKnownSites(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("cmd", "run.go"):                        true, // the PSP runner CLI — the one cached lane
		filepath.Join("internal", "psp", "cache_executor.go"): true, // the definition itself
	}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "bin" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(string(raw), "NewCacheExecutor(") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		if !allowed[rel] {
			t.Errorf("%s constructs a CacheExecutor — new wiring site outside the allowlist; if intentional (NOT the eval-gate lane, D-4), extend the allowlist with a comment", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
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
