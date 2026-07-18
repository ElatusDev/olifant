// Package kbtree abstracts the read-side of the knowledge-base tree that cite
// resolution walks, so a cite can be resolved either against a working-tree
// checkout (fsTree — the historical behavior) or directly against a git ref's
// blobs (gitTree — olifant#90 / EV-F1), decoupling the cite/eval gate from any
// checkout state and retiring the pinned-worktree ceremony.
//
// Only the KB side is abstracted: repo-path cites (source files) keep their
// real-platformRoot filesystem resolution (D227). All paths are KB-relative,
// slash-separated. fsTree is a verbatim wrapper of the os.* calls the resolver
// and validator used before this package existed, so the default (working-tree)
// path is byte-identical (olifant#90 AC4).
package kbtree

// Tree is the read-only view of a knowledge-base tree that cite resolution
// needs. Implementations must return KB-relative, slash-separated paths.
type Tree interface {
	// ReadFile returns the bytes of the KB-relative file, or an error if it
	// does not exist / cannot be read.
	ReadFile(rel string) ([]byte, error)
	// Exists reports whether the KB-relative path is a readable file (not a dir).
	Exists(rel string) bool
	// Glob returns the KB-relative paths matching a single-level glob pattern
	// (path.Match semantics, e.g. "standards/*.md").
	Glob(pattern string) ([]string, error)
	// List returns the KB-relative paths of every file under dir, recursively.
	List(dir string) ([]string, error)
	// BlobSHAs maps KB-relative path → git blob SHA for tracked files; empty
	// when the tree is not git-backed.
	BlobSHAs() map[string]string
}
