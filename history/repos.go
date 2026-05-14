package history

import "path/filepath"

// DefaultRepos returns the platform's seven repos rooted under
// platformRoot, ordered smallest-first so failures surface quickly
// and verbose output stays readable.
//
// Mirrors internal/repos.DefaultRepos but kept independent — the
// history package's scoping needs may diverge later (cross-cutting
// scopes, multi-scope commits, etc.).
func DefaultRepos(platformRoot string) []RepoSpec {
	return []RepoSpec{
		{Name: "infra", Path: filepath.Join(platformRoot, "infra"), Scope: "infra"},
		{Name: "core-api-e2e", Path: filepath.Join(platformRoot, "core-api-e2e"), Scope: "e2e"},
		{Name: "akademia-plus-go", Path: filepath.Join(platformRoot, "akademia-plus-go"), Scope: "mobile"},
		{Name: "akademia-plus-central", Path: filepath.Join(platformRoot, "akademia-plus-central"), Scope: "mobile"},
		{Name: "elatusdev-web", Path: filepath.Join(platformRoot, "elatusdev-web"), Scope: "webapp"},
		{Name: "akademia-plus-web", Path: filepath.Join(platformRoot, "akademia-plus-web"), Scope: "webapp"},
		{Name: "core-api", Path: filepath.Join(platformRoot, "core-api"), Scope: "backend"},
	}
}
