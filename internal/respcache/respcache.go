// Package respcache is a content-addressed, on-disk cache of model
// responses — the durable reuse layer the platform owns, independent of any
// backend cache TTL (epic #119 S1). Layout mirrors the digest cache
// precedent: objects under `objects/<ab>/<sha256>.json` plus an append-only
// `log.ndjson` hit/miss ledger, rooted OUTSIDE every corpus walk
// (`~/.olifant/responses` by default) so a cached model response can never
// become retrievable truth (AP184 / workflow D-2).
//
// Writes are crash-safe and race-benign: the same key always serializes to
// the same object path, and objects land via temp+rename, so concurrent
// sessions never expose a partial file — the last completed writer wins
// (workflow D-5).
package respcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// EnvDir overrides the default store root when set.
const EnvDir = "OLIFANT_RESPONSE_CACHE_DIR"

// Key identifies one cacheable model call. Every field participates in the
// digest: a change to any of them — including a model version bump — must
// dead-end previously stored entries (workflow D-3).
type Key struct {
	Prompt       string
	System       string
	Model        string
	ModelVersion string
	Effort       string
	SchemaJSON   string // canonical JSON of the output schema; "" when unconstrained
}

// SHA returns the hex SHA-256 of the key's canonical serialization. Fields
// are length-prefixed (netstring style) in a fixed order so no two distinct
// keys can collide by field-boundary ambiguity.
func (k Key) SHA() string {
	h := sha256.New()
	for _, f := range []string{k.Prompt, k.System, k.Model, k.ModelVersion, k.Effort, k.SchemaJSON} {
		h.Write([]byte(strconv.Itoa(len(f)) + ":"))
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Entry is one stored response. Payload is an opaque JSON document owned by
// the caller (the PSP layer stores its full Response there) — respcache
// never interprets it. The Anthropic cache token counts are pass-through
// measurements (claudecli usage fields); 0 on the Ollama lane.
type Entry struct {
	Payload             json.RawMessage `json:"payload"`
	Model               string          `json:"model"`
	ModelVersion        string          `json:"model_version,omitempty"`
	CacheCreationTokens int             `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int             `json:"cache_read_tokens,omitempty"`
	CreatedUnix         int64           `json:"created_unix"`
}

// Record is one ledger line in log.ndjson. Events: hit/miss from Get, store
// from Put, invalidate from Delete, drift when a hit's payload later failed
// to decode at the caller (served responses = hits − drifts).
type Record struct {
	TS                  string `json:"ts"`
	KeySHA              string `json:"key_sha"`
	Event               string `json:"event"` // hit | miss | store | invalidate | drift
	Model               string `json:"model"`
	ModelVersion        string `json:"model_version,omitempty"`
	CacheCreationTokens int    `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int    `json:"cache_read_tokens,omitempty"`
}

// Store is a response cache rooted at a single directory.
type Store struct {
	root string
	mu   sync.Mutex // serializes ledger appends within this process
}

// DefaultRoot resolves the store root: $OLIFANT_RESPONSE_CACHE_DIR when set,
// else ~/.olifant/responses.
func DefaultRoot() (string, error) {
	if dir := os.Getenv(EnvDir); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("respcache: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".olifant", "responses"), nil
}

// Open returns a Store rooted at root; root == "" resolves via DefaultRoot.
// The directory tree is created lazily on first write, so opening a store
// never mutates the filesystem.
func Open(root string) (*Store, error) {
	if root == "" {
		resolved, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	return &Store{root: root}, nil
}

// Root returns the resolved store root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) objectPath(sha string) string {
	return filepath.Join(s.root, "objects", sha[:2], sha+".json")
}

// Get looks up a key. Both outcomes are ledgered (hit / miss); a corrupt
// object is treated as a miss so the caller regenerates and overwrites it.
func (s *Store) Get(k Key) (*Entry, bool) {
	sha := k.SHA()
	raw, err := os.ReadFile(s.objectPath(sha))
	if err == nil {
		var e Entry
		if jerr := json.Unmarshal(raw, &e); jerr == nil {
			s.appendRecord(Record{KeySHA: sha, Event: "hit", Model: k.Model, ModelVersion: k.ModelVersion,
				CacheCreationTokens: e.CacheCreationTokens, CacheReadTokens: e.CacheReadTokens})
			return &e, true
		}
	}
	s.appendRecord(Record{KeySHA: sha, Event: "miss", Model: k.Model, ModelVersion: k.ModelVersion})
	return nil, false
}

// Put stores an entry under the key via temp+rename and ledgers the store
// event. Same key ⇒ same object path; concurrent writers race benignly.
func (s *Store) Put(k Key, e Entry) error {
	if e.CreatedUnix == 0 {
		e.CreatedUnix = time.Now().Unix()
	}
	if e.Model == "" {
		e.Model = k.Model
	}
	if e.ModelVersion == "" {
		e.ModelVersion = k.ModelVersion
	}
	sha := k.SHA()
	path := s.objectPath(sha)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("respcache: mkdir objects shard: %w", err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("respcache: marshal entry: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+sha+"-*")
	if err != nil {
		return fmt.Errorf("respcache: create temp object: %w", err)
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(raw); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("respcache: write temp object: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("respcache: close temp object: %w", cerr)
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("respcache: rename object into place: %w", rerr)
	}
	s.appendRecord(Record{KeySHA: sha, Event: "store", Model: e.Model, ModelVersion: e.ModelVersion,
		CacheCreationTokens: e.CacheCreationTokens, CacheReadTokens: e.CacheReadTokens})
	return nil
}

// Delete removes the object stored under the key (no-op if absent) and
// ledgers an invalidate event. The runner calls this when a served or fresh
// response fails step validation — a NAKed response must not be replayed on
// the next run.
func (s *Store) Delete(k Key) {
	sha := k.SHA()
	if err := os.Remove(s.objectPath(sha)); err != nil {
		return // nothing was cached (or removal failed) — no phantom invalidations
	}
	s.appendRecord(Record{KeySHA: sha, Event: "invalidate", Model: k.Model, ModelVersion: k.ModelVersion})
}

// RecordDrift ledgers that a hit's payload failed to decode at the caller
// and a live call was made instead — keeps S2 hit-rate honest.
func (s *Store) RecordDrift(k Key) {
	s.appendRecord(Record{KeySHA: k.SHA(), Event: "drift", Model: k.Model, ModelVersion: k.ModelVersion})
}

// appendRecord appends one NDJSON line to the ledger. Ledger failures are
// deliberately swallowed: the ledger is observability, and a full disk or
// permission hiccup there must never break the serving path.
func (s *Store) appendRecord(r Record) {
	r.TS = time.Now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.root, "log.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
