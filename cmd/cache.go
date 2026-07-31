// Cache dispatches `olifant cache status|prune` — the operator surface over
// the S1 response cache (epic #119 S2). Read/delete only: the serving path
// lives in internal/psp; this command never touches keys or payload content.
package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/respcache"
)

// Cache dispatches the cache subcommands.
func Cache(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "olifant cache: usage: cache status [-json] | cache prune [--older-than <dur>] [--max-size <n[K|M|G]B>] [--all] [--dry-run]")
		return 2
	}
	switch args[0] {
	case "status":
		return cacheStatus(args[1:])
	case "prune":
		return cachePrune(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "olifant cache: unknown subcommand %q (want status|prune)\n", args[0])
		return 2
	}
}

func cacheStatus(args []string) int {
	fs := flag.NewFlagSet("cache status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	_ = fs.Parse(args)

	store, err := respcache.Open("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant cache status:", err)
		return 1
	}
	st, err := store.Stats()
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant cache status:", err)
		return 1
	}
	if *asJSON {
		out, merr := json.MarshalIndent(st, "", "  ")
		if merr != nil {
			fmt.Fprintln(os.Stderr, "olifant cache status:", merr)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	fmt.Printf("store: %s\n", store.Root())
	fmt.Printf("entries: %d (%s)", st.Entries, humanBytes(st.Bytes))
	if st.TempFiles > 0 {
		fmt.Printf("  temp-orphans: %d", st.TempFiles)
	}
	fmt.Println()
	fmt.Printf("ledger: hits=%d misses=%d stores=%d invalidates=%d drifts=%d prunes=%d\n",
		st.Hits, st.Misses, st.Stores, st.Invalidates, st.Drifts, st.Prunes)
	fmt.Printf("hit-rate: %.1f%% (served = hits − drifts, per D317)\n", st.HitRatePct)
	fmt.Printf("anthropic pass-through: cache_read=%d cache_creation=%d tokens\n", st.CacheReadTokens, st.CacheCreationTokens)
	return 0
}

func cachePrune(args []string) int {
	fs := flag.NewFlagSet("cache prune", flag.ExitOnError)
	olderThan := fs.String("older-than", "", "delete entries older than this Go duration (e.g. 720h)")
	maxSize := fs.String("max-size", "", "evict oldest-first until the store fits (e.g. 500MB, 2GB)")
	all := fs.Bool("all", false, "delete every entry (explicit escape hatch)")
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	_ = fs.Parse(args)

	opts := respcache.PruneOptions{All: *all, DryRun: *dryRun}
	if *olderThan != "" {
		d, derr := time.ParseDuration(*olderThan)
		if derr != nil || d <= 0 {
			fmt.Fprintf(os.Stderr, "olifant cache prune: bad --older-than %q (want a positive Go duration like 720h)\n", *olderThan)
			return 2
		}
		opts.OlderThan = d
	}
	if *maxSize != "" {
		n, perr := parseBytes(*maxSize)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "olifant cache prune:", perr)
			return 2
		}
		opts.MaxBytes = n
	}

	store, err := respcache.Open("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant cache prune:", err)
		return 1
	}
	res, err := store.Prune(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant cache prune:", err)
		return 1
	}
	verb, reapVerb := "deleted", "reaped"
	if res.DryRun {
		verb, reapVerb = "would delete", "would reap"
	}
	fmt.Printf("%s %d entries (%s reclaimed), %s %d temp orphans; %d entries (%s) remain\n",
		verb, res.Deleted, humanBytes(res.ReclaimedBytes), reapVerb, res.TempReaped, res.Remaining, humanBytes(res.RemainingBytes))
	return 0
}

// parseBytes accepts "500MB", "2GB", "1024KB", or a bare byte count.
func parseBytes(s string) (int64, error) {
	up := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(up, u.suffix) {
			mult = u.mult
			up = strings.TrimSuffix(up, u.suffix)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(up), 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("bad size %q (want e.g. 500MB, 2GB, or bytes)", s)
	}
	return n * mult, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
