package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest is the on-disk record of "what olifant has seen" per repo.
// It survives across scans so subsequent retrain runs can pick up
// incrementally instead of re-walking the entire since-date floor.
//
// On-disk path (default): knowledge-base/short-term/history-manifest.yaml
type Manifest struct {
	BuilderVersion string         `yaml:"builder_version"`
	LastRunAt      string         `yaml:"last_run_at"`
	SinceFloor     string         `yaml:"since_floor"`
	Repos          []RepoManifest `yaml:"repos"`
}

// RepoManifest captures one repo's last-seen state plus a one-run
// delta (commits/snapshots emitted in the most recent run). Cumulative
// totals are deliberately NOT tracked — `git log` is authoritative.
type RepoManifest struct {
	Name            string  `yaml:"name"`
	Scope           string  `yaml:"scope"`
	LastSHA         string  `yaml:"last_sha"`
	LastCommittedAt string  `yaml:"last_committed_at"`
	LastRun         RunDelta `yaml:"last_run"`
}

// RunDelta records what the most recent run added for one repo.
type RunDelta struct {
	CommitsAdded   int `yaml:"commits_added"`
	SnapshotsAdded int `yaml:"snapshots_added"`
}

// LoadManifest reads the manifest at path. Returns an empty manifest
// (not an error) if the file does not exist — first-run case.
// Unparseable manifests return an error so accidents don't silently
// trigger a full re-scan.
func LoadManifest(path string) (*Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{BuilderVersion: BuilderVersion}, nil
		}
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.BuilderVersion == "" {
		m.BuilderVersion = BuilderVersion
	}
	return &m, nil
}

// SaveManifest writes the manifest to path atomically (temp file +
// rename) so a crashed write doesn't corrupt the file.
func SaveManifest(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	// Keep repo entries deterministic.
	sort.Slice(m.Repos, func(i, j int) bool { return m.Repos[i].Name < m.Repos[j].Name })

	body, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// LastSHA returns the recorded last_sha for repoName, or "" if the
// repo has never been scanned (first-run case for that repo).
func (m *Manifest) LastSHA(repoName string) string {
	for i := range m.Repos {
		if m.Repos[i].Name == repoName {
			return m.Repos[i].LastSHA
		}
	}
	return ""
}

// UpdateRepo records the latest scan state for one repo. Overwrites
// any existing entry for that repo name.
func (m *Manifest) UpdateRepo(name, scope, lastSHA string, lastCommittedAt time.Time, delta RunDelta) {
	entry := RepoManifest{
		Name:            name,
		Scope:           scope,
		LastSHA:         lastSHA,
		LastCommittedAt: lastCommittedAt.UTC().Format(time.RFC3339),
		LastRun:         delta,
	}
	for i := range m.Repos {
		if m.Repos[i].Name == name {
			m.Repos[i] = entry
			return
		}
	}
	m.Repos = append(m.Repos, entry)
}
