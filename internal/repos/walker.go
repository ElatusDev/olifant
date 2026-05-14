// Package repos walks platform source repositories, chunks each tracked file,
// and (via Ingester) embeds + upserts the chunks into ChromaDB `code_<scope>`
// collections.
//
// Files are enumerated via `git ls-files -s` so .gitignore is respected —
// node_modules/, target/, dist/, build/, etc. are automatically excluded.
package repos

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SourceFile is one tracked file in a repo, ready to be chunked.
type SourceFile struct {
	RepoDir  string // absolute path to the repo root
	RepoName string // e.g., "core-api"
	RelPath  string // path within the repo, forward-slash, e.g., "src/Foo.java"
	SHA      string // git blob SHA for the version at HEAD
	Bytes    []byte // file content
	Language string // derived from extension
}

// MaxFileBytes — files larger than this are skipped (likely auto-generated
// blobs, datasets, or accidentally committed binaries).
const MaxFileBytes = 2 * 1024 * 1024 // 2 MB

// Walk enumerates every tracked, text-like file in a repo.
// `repoDir` is the absolute path to the repo's working tree.
// `repoName` becomes the metadata tag.
func Walk(repoDir, repoName string) ([]SourceFile, error) {
	pairs, err := gitLsFilesSHAs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("git ls-files %s: %w", repoDir, err)
	}

	out := make([]SourceFile, 0, len(pairs))
	for relPath, sha := range pairs {
		abs := filepath.Join(repoDir, relPath)
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Size() > MaxFileBytes {
			continue
		}
		// Read first 4 KB, detect binary, then read full if text.
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		head := make([]byte, 4096)
		n, _ := io.ReadFull(f, head)
		head = head[:n]
		isBinary := looksBinary(head)
		if isBinary {
			f.Close()
			continue
		}
		// Rewind + read full.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		body, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			continue
		}
		out = append(out, SourceFile{
			RepoDir:  repoDir,
			RepoName: repoName,
			RelPath:  relPath,
			SHA:      sha,
			Bytes:    body,
			Language: languageForExt(filepath.Ext(relPath)),
		})
	}
	return out, nil
}

// looksBinary returns true if a NUL byte appears in the head — a robust
// heuristic for "not a text file". Catches images, archives, compiled
// artifacts, fonts, etc.
func looksBinary(head []byte) bool {
	return bytes.IndexByte(head, 0) >= 0
}

// languageForExt is a best-effort tag for metadata + UI. Not exhaustive;
// unknown extensions become "text".
func languageForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".swift":
		return "swift"
	case ".rs":
		return "rust"
	case ".tf", ".tfvars", ".hcl":
		return "terraform"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".md", ".mdx":
		return "markdown"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".dockerfile", "":
		// Special-case Dockerfile etc. handled by basename in caller if needed.
		return "text"
	case ".toml":
		return "toml"
	case ".ini":
		return "ini"
	case ".properties":
		return "properties"
	default:
		return "text"
	}
}

// gitLsFilesSHAs runs `git ls-files -s` in repoRoot and returns a map of
// relPath → blob SHA. Returns an empty map (no error) if not a git repo.
func gitLsFilesSHAs(repoRoot string) (map[string]string, error) {
	out := map[string]string{}
	cmd := exec.Command("git", "ls-files", "-s")
	cmd.Dir = repoRoot
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return out, nil
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		// <mode> SP <sha> SP <stage> TAB <path>
		line := scanner.Text()
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		left := line[:tab]
		path := line[tab+1:]
		parts := strings.Fields(left)
		if len(parts) < 2 {
			continue
		}
		out[filepath.ToSlash(path)] = parts[1]
	}
	_ = cmd.Wait()
	return out, nil
}
