package history

import (
	"regexp"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// citePatterns are compiled once and matched against every commit
// message body. The set mirrors the artifact-ID dialects called out
// in internal/challenge/runner.go's retrieval contract.
var citePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bD[0-9]{1,4}\b`),                   // D17, D122
	regexp.MustCompile(`\bAP[0-9]{1,4}\b`),                  // AP14, AP85
	regexp.MustCompile(`\b[A-Z]{2,4}-[A-Z]?[0-9]+[a-z]?\b`), // SB-04, WA-L13, TBU-22, RK-06b
	regexp.MustCompile(`\bCI[0-9]{1,4}\b`),                  // CI1
	regexp.MustCompile(`\bPC[0-9]{1,4}\b`),                  // PC3
}

// attributionPatterns strip lines that propagate Claude/Anthropic
// attribution into training data. Per platform feedback: training on
// these would teach the model to reproduce the pattern.
var attributionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^.*\bco-authored-by:.*\b(claude|anthropic|claude-code)\b.*$`),
	regexp.MustCompile(`(?im)^.*🤖.*generated.*claude.*$`),
	regexp.MustCompile(`(?im)^.*\bnoreply@anthropic\.com\b.*$`),
}

// collapseBlanks collapses 2+ consecutive blank lines into one.
var collapseBlanks = regexp.MustCompile(`\n{3,}`)

// Parse converts one go-git commit into the in-memory CommitRecord.
// repo+scope label the commit; cfg drives content/diff caps.
//
// Returns (nil, nil) — never. Always returns a populated record (or
// a partial one on patch failure). Callers should treat NumParents()==0
// as "skip" upstream.
func Parse(c *object.Commit, repo, scope string, cfg ScanConfig) (*CommitRecord, error) {
	contentCap := cfg.ContentCapBytes
	if contentCap <= 0 {
		contentCap = DefaultContentCapBytes
	}
	diffCap := cfg.DiffCapBytes
	if diffCap <= 0 {
		diffCap = DefaultDiffCapBytes
	}

	subject, body := splitMessage(c.Message)
	body = stripAttribution(body)
	cites := extractCites(c.Message) // extract before stripping — still want SI-19 etc.

	rec := &CommitRecord{
		Repo:        repo,
		Scope:       scope,
		SHA:         c.Hash.String(),
		ShortSHA:    c.Hash.String()[:7],
		Author:      c.Author.Name + " <" + c.Author.Email + ">",
		CommittedAt: c.Committer.When.UTC(),
		Subject:     subject,
		Body:        body,
		CiteIDs:     cites,
	}

	// Initial commit (no parent) — no diff. Caller handles skip.
	if c.NumParents() == 0 {
		return rec, nil
	}

	parent, err := c.Parent(0)
	if err != nil {
		return rec, err
	}
	rec.ParentSHA = parent.Hash.String()

	patch, err := parent.Patch(c)
	if err != nil {
		// Patch failure (binary/large/edge case): emit metadata-only.
		return rec, nil
	}

	rec.Files, rec.Snapshots = extractFilesAndSnapshots(c, parent, patch, contentCap, diffCap)
	return rec, nil
}

// splitMessage breaks a commit message into (subject, body). First
// line is subject, everything after the first blank line is body.
func splitMessage(msg string) (subject, body string) {
	msg = strings.TrimRight(msg, "\n")
	nl := strings.Index(msg, "\n")
	if nl < 0 {
		return strings.TrimSpace(msg), ""
	}
	subject = strings.TrimSpace(msg[:nl])
	body = strings.TrimSpace(msg[nl+1:])
	return subject, body
}

// stripAttribution removes any line matching the Claude/Anthropic
// attribution patterns and collapses resulting blank-line runs.
func stripAttribution(body string) string {
	for _, re := range attributionPatterns {
		body = re.ReplaceAllString(body, "")
	}
	body = collapseBlanks.ReplaceAllString(body, "\n\n")
	return strings.TrimSpace(body)
}

// extractCites scans a commit message for artifact IDs and returns
// them deduplicated, in first-seen order per regex.
func extractCites(msg string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, re := range citePatterns {
		for _, m := range re.FindAllString(msg, -1) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// extractFilesAndSnapshots walks every FilePatch in the commit, reads
// the post-commit file content (or pre-deletion content for deletes),
// captures the full per-file unified diff with context lines, and
// records line-delta counts.
func extractFilesAndSnapshots(c, parent *object.Commit, patch *object.Patch, contentCap, diffCap int) ([]FileTouch, []FileSnapshot) {
	fps := patch.FilePatches()
	files := make([]FileTouch, 0, len(fps))
	snaps := make([]FileSnapshot, 0, len(fps))

	// Pre-render the full unified diff once; split per-file by the
	// "diff --git " boundary. go-git emits sections in FilePatches
	// order, so positional zipping is safe.
	perFileDiffs := splitUnifiedDiff(patch.String())

	for i, fp := range fps {
		from, to := fp.Files()
		var path, oldPath, status string
		switch {
		case from == nil && to != nil:
			path = to.Path()
			status = "added"
		case from != nil && to == nil:
			path = from.Path()
			status = "deleted"
		case from != nil && to != nil && from.Path() != to.Path():
			path = to.Path()
			oldPath = from.Path()
			status = "renamed"
		case from != nil && to != nil:
			path = to.Path()
			status = "modified"
		default:
			continue
		}
		if path == "" {
			continue
		}

		add, del := 0, 0
		for _, chunk := range fp.Chunks() {
			switch chunk.Type() {
			case 1: // diff.Add
				add += countLines(chunk.Content())
			case 2: // diff.Delete
				del += countLines(chunk.Content())
			}
		}
		files = append(files, FileTouch{
			Path:      path,
			Status:    status,
			OldPath:   oldPath,
			Additions: add,
			Deletions: del,
		})

		// Resolve per-file unified diff (positional zip with go-git's
		// FilePatches ordering).
		var diffStr string
		if i < len(perFileDiffs) {
			diffStr = perFileDiffs[i]
		}

		snap := FileSnapshot{
			Path:        path,
			OldPath:     oldPath,
			Status:      status,
			DiffSize:    len(diffStr),
			DiffUnified: capBytes(diffStr, diffCap),
		}
		snap.DiffTruncated = len(diffStr) > diffCap

		// Read file content at the relevant tree.
		// added/modified/renamed → c.Tree() at `path`.
		// deleted               → parent.Tree() at `path` (pre-deletion).
		if !fp.IsBinary() {
			var rawContent string
			if status == "deleted" {
				rawContent = readFileAtTree(parent, path)
			} else {
				rawContent = readFileAtTree(c, path)
			}
			snap.ContentSize = len(rawContent)
			snap.Content = capBytes(rawContent, contentCap)
			snap.ContentTruncated = len(rawContent) > contentCap
		} else {
			snap.Content = "[binary file]"
			snap.ContentSize = 0
		}

		snaps = append(snaps, snap)
	}

	// Deterministic file ordering for downstream consumers.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Path < snaps[j].Path })
	return files, snaps
}

// readFileAtTree returns the file at path in the commit's tree, or
// "" on any error (missing, large blob fail, etc.). Errors are not
// propagated — the scan is best-effort per file.
func readFileAtTree(c *object.Commit, path string) string {
	tree, err := c.Tree()
	if err != nil {
		return ""
	}
	file, err := tree.File(path)
	if err != nil {
		return ""
	}
	content, err := file.Contents()
	if err != nil {
		return ""
	}
	return content
}

// splitDiffRE matches the per-file section boundary in go-git's
// patch.String() output, at line start.
var splitDiffRE = regexp.MustCompile(`(?m)^diff --git `)

// splitUnifiedDiff partitions go-git's patch.String() output into
// one entry per file, matching the FilePatches ordering. Each entry
// starts with "diff --git ".
func splitUnifiedDiff(full string) []string {
	if full == "" {
		return nil
	}
	locs := splitDiffRE.FindAllStringIndex(full, -1)
	if len(locs) == 0 {
		return nil
	}
	out := make([]string, 0, len(locs))
	for i, loc := range locs {
		start := loc[0]
		end := len(full)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, strings.TrimRight(full[start:end], "\n"))
	}
	return out
}

// capBytes truncates s to maxBytes at a UTF-8 boundary; if truncated,
// appends a marker noting the original byte count.
func capBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	// Trim to last newline below the cap so output stays line-aligned.
	if nl := strings.LastIndexByte(s[:end], '\n'); nl > 0 {
		end = nl
	}
	return s[:end] + "\n... [truncated, original " + intToStr(len(s)) + " bytes]"
}

func intToStr(n int) string {
	// stdlib-only, avoid strconv import bloat in this file
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
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

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
