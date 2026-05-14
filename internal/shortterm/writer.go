package shortterm

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// NewTurnID produces a chronologically-sortable ID: `2026-05-14T14-22-03Z-<6char>`.
// The hash entropy is derived from (ts + content seed) so two turns at the same
// second still get distinct IDs.
func NewTurnID(ts time.Time, contentSeed string) string {
	stamp := ts.UTC().Format("2006-01-02T15-04-05Z")
	h := sha1.New()
	h.Write([]byte(stamp))
	h.Write([]byte{0})
	h.Write([]byte(contentSeed))
	hashShort := hex.EncodeToString(h.Sum(nil))[:6]
	return fmt.Sprintf("%s-%s", stamp, hashShort)
}

// Write serialises a record to <kbRoot>/short-term/turns/<turn_id>.yaml.
// Returns the absolute path written.
func Write(kbRoot string, rec *TurnRecord) (string, error) {
	if rec.TurnID == "" {
		return "", fmt.Errorf("shortterm.Write: TurnID is empty")
	}
	dir := filepath.Join(kbRoot, "short-term", "turns")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	abs := filepath.Join(dir, rec.TurnID+".yaml")

	// Header comment makes the file self-identify when read raw.
	header := fmt.Sprintf("# Olifant turn record — append-only short-term memory.\n# Schema: ../README.md\n# Written: %s\n\n",
		time.Now().UTC().Format(time.RFC3339))

	body, err := yaml.Marshal(rec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(header+string(body)), 0o644); err != nil {
		return "", err
	}
	return abs, nil
}
