package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// olifantSystemPrompt is the fixed system message used for every
// emitted training example.
const olifantSystemPrompt = "You are Olifant — domain expert for ElatusDev/AkademiaPlus."

// BuildCommitSummary turns a CommitRecord into the per-commit summary
// JSONL row. Captures subject + body + cite IDs + a capped
// files-touched listing (top N by changed lines + overflow marker).
func BuildCommitSummary(rec *CommitRecord, filesListCap int) JSONLExample {
	if filesListCap <= 0 {
		filesListCap = DefaultFilesListCap
	}

	userTurn := fmt.Sprintf("What changed in commit %s on %s, and why?", rec.ShortSHA, rec.Repo)

	var b strings.Builder
	b.WriteString(rec.Subject)
	if rec.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(rec.Body)
	}

	if len(rec.Files) > 0 {
		// Rank files by changed lines (desc), cap at filesListCap with
		// overflow marker.
		ranked := make([]FileTouch, len(rec.Files))
		copy(ranked, rec.Files)
		sort.SliceStable(ranked, func(i, j int) bool {
			ci := ranked[i].Additions + ranked[i].Deletions
			cj := ranked[j].Additions + ranked[j].Deletions
			if ci != cj {
				return ci > cj
			}
			return ranked[i].Path < ranked[j].Path
		})
		overflow := 0
		if len(ranked) > filesListCap {
			overflow = len(ranked) - filesListCap
			ranked = ranked[:filesListCap]
		}
		b.WriteString("\n\nFiles touched")
		if overflow > 0 {
			fmt.Fprintf(&b, " (top %d by changed lines; %d more)", filesListCap, overflow)
		}
		b.WriteString(":")
		for _, f := range ranked {
			fmt.Fprintf(&b, "\n- %s (%s, +%d/-%d)", f.Path, f.Status, f.Additions, f.Deletions)
			if f.OldPath != "" {
				fmt.Fprintf(&b, " ← %s", f.OldPath)
			}
		}
	}

	meta := map[string]string{
		"sha":          rec.SHA,
		"author":       rec.Author,
		"committed_at": rec.CommittedAt.Format("2006-01-02T15:04:05Z"),
		"parent_sha":   rec.ParentSHA,
	}
	if len(rec.CiteIDs) > 0 {
		meta["cite_ids"] = strings.Join(rec.CiteIDs, ",")
	}

	return JSONLExample{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: userTurn},
			{Role: "assistant", Content: b.String()},
		},
		Tier:     3,
		Scope:    rec.Scope,
		Source:   rec.Repo + "@" + rec.ShortSHA,
		Role:     "validate",
		Family:   "commit-summary",
		Metadata: meta,
	}
}

// BuildFileSnapshot turns one (commit, file) pair into a JSONL row
// carrying the full file content at the commit tree + the full
// per-file unified diff + commit context. This is the pattern +
// evolution signal — the model sees both what the code looks like
// and how it got there.
func BuildFileSnapshot(rec *CommitRecord, snap FileSnapshot) JSONLExample {
	userTurn := fmt.Sprintf(
		"What does the file `%s` look like in `%s` as of commit %s (%q), and how did it change?",
		snap.Path, rec.Repo, rec.ShortSHA, rec.Subject,
	)

	var b strings.Builder
	b.WriteString("Status: ")
	b.WriteString(snap.Status)
	if snap.OldPath != "" {
		fmt.Fprintf(&b, " (from %s)", snap.OldPath)
	}
	b.WriteString("\n\nContent at this commit:\n```\n")
	b.WriteString(snap.Content)
	if !strings.HasSuffix(snap.Content, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("```")

	if snap.DiffUnified != "" {
		b.WriteString("\n\nUnified diff in this commit:\n```diff\n")
		b.WriteString(snap.DiffUnified)
		if !strings.HasSuffix(snap.DiffUnified, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```")
	}

	b.WriteString("\n\nCommit context: ")
	b.WriteString(rec.Subject)
	if rec.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(rec.Body)
	}

	meta := map[string]string{
		"sha":              rec.SHA,
		"parent_sha":       rec.ParentSHA,
		"author":           rec.Author,
		"committed_at":     rec.CommittedAt.Format("2006-01-02T15:04:05Z"),
		"path":             snap.Path,
		"status":           snap.Status,
		"content_size":     intToStr(snap.ContentSize),
		"content_truncated": boolStr(snap.ContentTruncated),
		"diff_size":        intToStr(snap.DiffSize),
		"diff_truncated":   boolStr(snap.DiffTruncated),
	}
	if snap.OldPath != "" {
		meta["old_path"] = snap.OldPath
	}
	if len(rec.CiteIDs) > 0 {
		meta["cite_ids"] = strings.Join(rec.CiteIDs, ",")
	}

	return JSONLExample{
		System: olifantSystemPrompt,
		Messages: []ChatMessage{
			{Role: "user", Content: userTurn},
			{Role: "assistant", Content: b.String()},
		},
		Tier:     3,
		Scope:    rec.Scope,
		Source:   rec.Repo + "@" + rec.ShortSHA + ":" + snap.Path,
		Role:     "domain",
		Family:   "file-snapshot",
		Metadata: meta,
	}
}

// EmitJSONL writes two JSONL files per repo:
//   - <outDir>/<repo>.commits.jsonl    (one row per commit)
//   - <outDir>/<repo>.snapshots.jsonl  (one row per (commit, file))
//
// Records are written in walked order (most-recent-first).
func EmitJSONL(outDir, repo string, records []*CommitRecord, filesListCap int) (commitRows, snapshotRows int, err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	commitsPath := filepath.Join(outDir, repo+".commits.jsonl")
	cf, err := os.Create(commitsPath)
	if err != nil {
		return 0, 0, fmt.Errorf("create %s: %w", commitsPath, err)
	}
	defer cf.Close()
	cenc := json.NewEncoder(cf)
	cenc.SetEscapeHTML(false)

	snapshotsPath := filepath.Join(outDir, repo+".snapshots.jsonl")
	sf, err := os.Create(snapshotsPath)
	if err != nil {
		return 0, 0, fmt.Errorf("create %s: %w", snapshotsPath, err)
	}
	defer sf.Close()
	senc := json.NewEncoder(sf)
	senc.SetEscapeHTML(false)

	for _, rec := range records {
		summary := BuildCommitSummary(rec, filesListCap)
		if err := cenc.Encode(&summary); err != nil {
			return commitRows, snapshotRows, fmt.Errorf("encode commit %s: %w", rec.SHA, err)
		}
		commitRows++

		for _, snap := range rec.Snapshots {
			ex := BuildFileSnapshot(rec, snap)
			if err := senc.Encode(&ex); err != nil {
				return commitRows, snapshotRows, fmt.Errorf("encode snapshot %s:%s: %w", rec.SHA, snap.Path, err)
			}
			snapshotRows++
		}
	}
	return commitRows, snapshotRows, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
