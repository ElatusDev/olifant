package history

import (
	"context"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Walk opens repoPath, iterates HEAD's first-parent history, and
// returns the CommitRecord list for every commit newer than stopAtSHA
// (exclusive) AND on or after cfg.Since. Pass stopAtSHA="" for a full
// scan. Initial commits (no parent) are skipped — they carry no diff
// signal. Errors from individual commit parses are logged via the
// verbose flag and do not abort the walk.
//
// If stopAtSHA is set but not found anywhere in the iter's window
// (e.g., force-push rewrote history), the iter naturally exhausts at
// the since-floor and the result equals a full scan from the floor.
// This is self-healing — no special detection needed.
func Walk(ctx context.Context, repoPath, repoName, scope, stopAtSHA string, cfg ScanConfig) ([]*CommitRecord, int, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, 0, fmt.Errorf("git open %s: %w", repoPath, err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, 0, fmt.Errorf("git head %s: %w", repoPath, err)
	}

	var since *time.Time
	if !cfg.Since.IsZero() {
		s := cfg.Since
		since = &s
	}

	iter, err := repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Since: since,
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("git log %s: %w", repoPath, err)
	}
	defer iter.Close()

	out := make([]*CommitRecord, 0, 256)
	walked := 0
	// errStopWalk is a sentinel — go-git ForEach treats any non-nil
	// return as a terminate signal; we re-wrap below.
	errStopWalk := fmt.Errorf("history: reached stopAtSHA")
	err = iter.ForEach(func(c *object.Commit) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if stopAtSHA != "" && c.Hash.String() == stopAtSHA {
			return errStopWalk
		}
		walked++
		if c.NumParents() == 0 {
			return nil
		}
		rec, perr := Parse(c, repoName, scope, cfg)
		if perr != nil {
			if cfg.Verbose {
				fmt.Printf("  warn: parse %s/%s: %v\n", repoName, c.Hash.String()[:7], perr)
			}
			return nil
		}
		if rec == nil {
			return nil
		}
		out = append(out, rec)
		return nil
	})
	if err != nil && err != errStopWalk {
		return out, walked, err
	}
	return out, walked, nil
}
