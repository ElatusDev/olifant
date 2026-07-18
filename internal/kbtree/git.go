package kbtree

import (
	"bytes"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// gitTree reads a knowledge-base tree from a git ref's blobs, decoupled from
// any working-tree checkout (olifant#90 / EV-F1). The full tree listing (paths
// + blob SHAs) is fetched once at construction via a single `git ls-tree -r`,
// so List/Glob/Exists/BlobSHAs cost no further forks (D-GR5, no fork storm);
// blob bodies are read on demand. All paths are ref-relative == KB-relative.
type gitTree struct {
	repoDir string
	ref     string
	shas    map[string]string // kb-rel path → blob sha (the cached listing)
}

// Git returns a Tree backed by ref's blobs in the git repo at repoDir. It errors
// if the ref is missing/unreadable — never a silent working-tree fallback
// (olifant#90 D-GR4/AC6). repoDir may be on any branch; only ref's blobs are read.
func Git(repoDir, ref string) (Tree, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("kbtree: empty git ref")
	}
	// A `-`-prefixed value would reach git as a FLAG, not a ref (argument
	// injection). Git refnames cannot begin with `-` (git-check-ref-format),
	// so rejecting is lossless (olifant#95 AC6, the #90 review nit).
	if strings.HasPrefix(strings.TrimSpace(ref), "-") {
		return nil, fmt.Errorf("kbtree: invalid git ref %q (must not start with '-')", ref)
	}
	// One `ls-tree -r <ref>` lists every tracked path + its blob sha. A bad ref
	// makes git exit non-zero — surfaced as a named error, not a fallback.
	out, err := runGit(repoDir, "ls-tree", "-r", ref)
	if err != nil {
		return nil, fmt.Errorf("kbtree: git ref %q not found in %s (git fetch first?): %w", ref, repoDir, err)
	}
	shas := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		// format: "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		shas[filepath.ToSlash(line[tab+1:])] = fields[2]
	}
	return &gitTree{repoDir: repoDir, ref: ref, shas: shas}, nil
}

func (t *gitTree) ReadFile(rel string) ([]byte, error) {
	rel = filepath.ToSlash(rel)
	if _, ok := t.shas[rel]; !ok {
		return nil, fmt.Errorf("kbtree: %s not in git ref %s", rel, t.ref)
	}
	return runGit(t.repoDir, "cat-file", "-p", t.ref+":"+rel)
}

func (t *gitTree) Exists(rel string) bool {
	_, ok := t.shas[filepath.ToSlash(rel)]
	return ok
}

func (t *gitTree) Glob(pattern string) ([]string, error) {
	pattern = filepath.ToSlash(pattern)
	var out []string
	for p := range t.shas {
		if ok, _ := path.Match(pattern, p); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (t *gitTree) List(dir string) ([]string, error) {
	prefix := filepath.ToSlash(dir)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var out []string
	for p := range t.shas {
		if prefix == "" || strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (t *gitTree) BlobSHAs() map[string]string {
	// Copy so callers can't mutate the cached listing.
	out := make(map[string]string, len(t.shas))
	for k, v := range t.shas {
		out[k] = v
	}
	return out
}

func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
