package corpus

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitLsFilesSHAs runs `git ls-files -s` inside repoRoot and returns a map of
// path (relative to repoRoot) → blob SHA. If repoRoot isn't a git repo, returns
// an empty map with no error — corpus build still works, source_sha is empty.
func gitLsFilesSHAs(repoRoot string) (map[string]string, error) {
	out := map[string]string{}
	cmd := exec.Command("git", "ls-files", "-s")
	cmd.Dir = repoRoot
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		// not a git repo or git missing — non-fatal
		return out, nil
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Format: <mode> SP <sha> SP <stage> TAB <path>
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
