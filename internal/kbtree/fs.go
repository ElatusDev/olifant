package kbtree

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// fsTree reads a knowledge-base tree from a working-directory checkout rooted
// at Root. It is a verbatim wrapper of the os.* calls the resolver/validator
// used before kbtree existed — the byte-identical anchor for olifant#90 AC4.
type fsTree struct {
	root string
}

// FS returns a Tree backed by the working-tree checkout at root. An empty root
// yields a tree whose reads all fail (Exists false) — the historical
// empty-kbRoot degradation.
func FS(root string) Tree { return fsTree{root: root} }

func (t fsTree) abs(rel string) string {
	return filepath.Join(t.root, filepath.FromSlash(rel))
}

func (t fsTree) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(t.abs(rel))
}

func (t fsTree) Exists(rel string) bool {
	if t.root == "" {
		return false
	}
	info, err := os.Stat(t.abs(rel))
	return err == nil && !info.IsDir()
}

func (t fsTree) Glob(pattern string) ([]string, error) {
	if t.root == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(t.root, filepath.FromSlash(pattern)))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, rErr := filepath.Rel(t.root, m)
		if rErr != nil {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

func (t fsTree) List(dir string) ([]string, error) {
	if t.root == "" {
		return nil, nil
	}
	base := t.abs(dir)
	if _, err := os.Stat(base); err != nil {
		return nil, nil // absent dir → empty (matches the validator's Stat-then-skip)
	}
	var out []string
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rErr := filepath.Rel(t.root, path)
		if rErr != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// BlobSHAs shells `git ls-files -s` in root — the historical gitBlobSHAs
// behavior. Empty when root is not a git checkout.
func (t fsTree) BlobSHAs() map[string]string {
	out := map[string]string{}
	if t.root == "" {
		return out
	}
	cmd := exec.Command("git", "ls-files", "-s")
	cmd.Dir = t.root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out
	}
	if err := cmd.Start(); err != nil {
		return out
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 2 {
			continue
		}
		out[filepath.ToSlash(line[tab+1:])] = fields[1]
	}
	_ = cmd.Wait()
	return out
}
