package repos

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ElatusDev/olifant/internal/corpus"
)

// Chunking constants — windowed v0. Language-aware later.
const (
	WindowLines  = 80 // lines per chunk
	OverlapLines = 20 // shared lines between adjacent chunks
)

// Chunk turns a SourceFile into one or more corpus.Chunk records suitable for
// upserting into Chroma. Each chunk:
//   - is at most WindowLines long
//   - overlaps the previous chunk by OverlapLines
//   - carries a breadcrumb header `[code/<lang>] <repo>/<rel>#L<a>-L<b>`
//   - has a content-derived chunk_id (stable across rebuilds)
func Chunk(file SourceFile, scope string) []corpus.Chunk {
	lines := splitLinesKeepEmpty(file.Bytes)
	if len(lines) == 0 {
		return nil
	}

	var out []corpus.Chunk
	step := WindowLines - OverlapLines
	if step <= 0 {
		step = WindowLines
	}
	for start := 0; start < len(lines); start += step {
		end := start + WindowLines
		if end > len(lines) {
			end = len(lines)
		}
		bodyLines := lines[start:end]
		body := buildBodyWithBreadcrumb(file, start+1, end, bodyLines)

		anchor := fmt.Sprintf("%s/%s#L%d-L%d", file.RepoName, file.RelPath, start+1, end)
		c := corpus.Chunk{
			ChunkID:      makeChunkID(file.RepoName+"/"+file.RelPath, anchor, body),
			Source:       file.RepoName + "/" + file.RelPath,
			SourceSHA:    file.SHA,
			SourceAnchor: anchor,
			Scope:        scope,
			DocType:      "code",
			Title:        fmt.Sprintf("%s:L%d-L%d", filepath.Base(file.RelPath), start+1, end),
			Body:         body,
			Metadata: corpus.ChunkMetadata{
				Section:  fmt.Sprintf("L%d-L%d", start+1, end),
				TechTags: []string{file.Language, file.RepoName},
				// CitesOutbound populated below if we detect dictionary IDs in the body
				CitesOutbound: corpus.ExtractCites(body),
			},
		}
		out = append(out, c)

		if end == len(lines) {
			break
		}
	}
	return out
}

// buildBodyWithBreadcrumb wraps the window content with a self-identifying
// header. The header is the FIRST line of the chunk body so that retrieval
// snippets always reveal source path + line range to the model.
func buildBodyWithBreadcrumb(file SourceFile, startLine, endLine int, lines []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[code/%s] %s/%s#L%d-L%d\n",
		file.Language, file.RepoName, file.RelPath, startLine, endLine)
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// splitLinesKeepEmpty splits a byte slice on '\n', preserving empty lines and
// dropping the trailing newline. Same semantics as bufio.Scanner with default
// SplitFunc on Lines.
func splitLinesKeepEmpty(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var out []string
	for scan.Scan() {
		out = append(out, scan.Text())
	}
	return out
}

// makeChunkID is content-stable so a rebuild on unchanged content produces
// the same ID — Chroma upsert overwrites without growth.
func makeChunkID(source, anchor, body string) string {
	h := sha1.New()
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(anchor))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}
