// Stats + Prune — the read/delete side of the response cache (epic #119 S2).
// Both operate directly on the object tree + NDJSON ledger; no index exists
// or is wanted at this scale (workflow D-1). The hit-rate formula lives in
// ONE place (HitRate, D-2): a ledgered "hit" whose payload later failed to
// decode is a drift — counting it as served would overstate the cache
// (AP288-class honesty).
package respcache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Stats summarizes the store: object tree on disk + ledger history.
type Stats struct {
	Entries     int   `json:"entries"`
	Bytes       int64 `json:"bytes"`
	TempFiles   int   `json:"temp_files"`
	Hits        int   `json:"hits"`
	Misses      int   `json:"misses"`
	Stores      int   `json:"stores"`
	Invalidates int   `json:"invalidates"`
	Drifts      int   `json:"drifts"`
	Prunes      int   `json:"prunes"`
	// HitRatePct is HitRate() over the ledger counts, as a percentage.
	HitRatePct float64 `json:"hit_rate_pct"`
	// Anthropic pass-through totals summed over ledger store records.
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
}

// HitRate returns the honest served fraction (0..1): drifts are hits that
// made a live call anyway, so they move from the numerator to the misses.
// Zero traffic returns 0.
func HitRate(hits, drifts, misses int) float64 {
	served := hits - drifts
	if served < 0 {
		served = 0
	}
	total := served + misses + drifts
	if total == 0 {
		return 0
	}
	return float64(served) / float64(total)
}

// Stats walks the object tree and scans the ledger. A missing store dir or
// ledger is an empty store, not an error.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	objRoot := filepath.Join(s.root, "objects")
	err := filepath.WalkDir(objRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil // raced with a concurrent prune — skip
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			st.TempFiles++
			return nil
		}
		if strings.HasSuffix(d.Name(), ".json") {
			st.Entries++
			st.Bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return st, fmt.Errorf("respcache: walk objects: %w", err)
	}

	f, err := os.Open(filepath.Join(s.root, "log.ndjson"))
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("respcache: open ledger: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r Record
		if jerr := json.Unmarshal(sc.Bytes(), &r); jerr != nil {
			continue // torn/foreign line — observability must not error
		}
		switch r.Event {
		case "hit":
			st.Hits++
		case "miss":
			st.Misses++
		case "store":
			st.Stores++
			st.CacheCreationTokens += r.CacheCreationTokens
			st.CacheReadTokens += r.CacheReadTokens
		case "invalidate":
			st.Invalidates++
		case "drift":
			st.Drifts++
		case "prune":
			st.Prunes++
		}
	}
	if serr := sc.Err(); serr != nil {
		return st, fmt.Errorf("respcache: scan ledger: %w", serr)
	}
	st.HitRatePct = 100 * HitRate(st.Hits, st.Drifts, st.Misses)
	return st, nil
}

// PruneOptions bound a prune run. At least one of OlderThan/MaxBytes/All
// must be set — Prune itself refuses an unbounded run (the CLI surfaces
// that refusal as a runtime error).
type PruneOptions struct {
	OlderThan time.Duration // delete entries with mtime older than this (0 = no age bound)
	MaxBytes  int64         // after the age pass, evict oldest-first until the store fits (0 = no size bound)
	All       bool          // delete every entry (explicit escape hatch)
	DryRun    bool          // compute + report, delete nothing
}

// PruneResult reports what a prune deleted (or would delete, for DryRun).
type PruneResult struct {
	Deleted        int   `json:"deleted"`
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
	TempReaped     int   `json:"temp_reaped"`
	Remaining      int   `json:"remaining"`
	RemainingBytes int64 `json:"remaining_bytes"`
	DryRun         bool  `json:"dry_run"`
}

// tmpReapAge guards in-flight writes: a temp file younger than this is a
// writer that has not renamed yet, never garbage (workflow D-3).
const tmpReapAge = time.Hour

type objInfo struct {
	path  string
	size  int64
	mtime time.Time
}

// Prune deletes entries per the options: age filter first, then
// oldest-first eviction until the store fits MaxBytes; orphaned `.tmp-*`
// older than tmpReapAge are always reaped. One `prune` summary record is
// appended on a non-dry run that deleted anything (workflow D-5).
func (s *Store) Prune(opts PruneOptions) (PruneResult, error) {
	res := PruneResult{DryRun: opts.DryRun}
	if opts.OlderThan == 0 && opts.MaxBytes == 0 && !opts.All {
		return res, fmt.Errorf("respcache: refusing unbounded prune — pass an age bound, a size bound, or All")
	}
	now := time.Now()
	var objects []objInfo
	objRoot := filepath.Join(s.root, "objects")
	err := filepath.WalkDir(objRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil // raced — skip
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			if now.Sub(info.ModTime()) > tmpReapAge {
				res.TempReaped++
				if !opts.DryRun {
					_ = os.Remove(path)
				}
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".json") {
			objects = append(objects, objInfo{path: path, size: info.Size(), mtime: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("respcache: walk objects: %w", err)
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].mtime.Before(objects[j].mtime) })

	var totalBytes int64
	for _, o := range objects {
		totalBytes += o.size
	}
	doomed := map[string]objInfo{}
	if opts.All {
		for _, o := range objects {
			doomed[o.path] = o
		}
	} else {
		if opts.OlderThan > 0 {
			cutoff := now.Add(-opts.OlderThan)
			for _, o := range objects {
				if o.mtime.Before(cutoff) {
					doomed[o.path] = o
				}
			}
		}
		if opts.MaxBytes > 0 {
			remaining := totalBytes
			for _, o := range doomed {
				remaining -= o.size
			}
			for _, o := range objects { // oldest first
				if remaining <= opts.MaxBytes {
					break
				}
				if _, dead := doomed[o.path]; dead {
					continue
				}
				doomed[o.path] = o
				remaining -= o.size
			}
		}
	}

	for _, o := range doomed {
		if opts.DryRun {
			res.Deleted++
			res.ReclaimedBytes += o.size
			continue
		}
		// Count only what was actually removed — a read-only mount or a
		// concurrent pruner must not inflate the report (AP288-class honesty).
		if err := os.Remove(o.path); err == nil {
			res.Deleted++
			res.ReclaimedBytes += o.size
		}
	}
	res.Remaining = len(objects) - res.Deleted
	res.RemainingBytes = totalBytes - res.ReclaimedBytes

	if !opts.DryRun && (res.Deleted > 0 || res.TempReaped > 0) {
		s.appendRecord(Record{Event: "prune", Note: fmt.Sprintf("deleted=%d reclaimed=%d tmp=%d", res.Deleted, res.ReclaimedBytes, res.TempReaped)})
	}
	return res, nil
}
