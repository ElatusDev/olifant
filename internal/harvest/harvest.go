// Package harvest mines the workflow-bookend usage signal — the reactions
// side-log (~/.olifant/reactions.jsonl) joined to the short-term turn ledger
// on turn_id — into HUMAN-GATED proposals: real-usage eval-case candidates,
// corpus-gap tickets, and dictionary-term candidates (BK-F5, D163).
//
// The three D-BK9 guards are structural here: harvest is PROPOSE-ONLY (it
// writes a report + its own cursor, never the KB/corpus/suite — D-HV1/2);
// it is LLM-free and deterministic (rule classification over
// reaction × verdict × unresolved_cites, no clock reads — D-HV6); and
// reactions are the labels — eval cases derive only from human accepts,
// rejects become investigate tickets, never silent drops (D-HV3, the D139
// anti-self-training guard).
//
// R3 handoff (D-OL2): the side-log also carries retro-time label lines
// (`phase:"retro"`, `label:"confirmed|wrong|partial|retracted"`). Effective
// signal per turn_id = the NEWEST retro line if any retro line exists, else
// the newest live line; `retracted` removes the turn from the labeled set.
package harvest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/shortterm"
)

// Reaction is one side-log line. Additive R3 fields are optional.
type Reaction struct {
	TurnID          string   `json:"turn_id"`
	TS              string   `json:"ts"`
	Subcommand      string   `json:"subcommand"`
	Verdict         string   `json:"verdict"`
	Reaction        string   `json:"reaction"`
	Phase           string   `json:"phase,omitempty"`
	Label           string   `json:"label,omitempty"`
	Note            string   `json:"note,omitempty"`
	UnresolvedCites []string `json:"unresolved_cites,omitempty"`
}

// Signal is one joined, effective (post-D-OL2) reaction with its turn record.
// Turn is nil when the ledger has no record for the id (reported, not fatal).
type Signal struct {
	Reaction Reaction
	Turn     *shortterm.TurnRecord
}

// LoadReactions parses the side-log, tolerating unknown fields and blank
// lines. Malformed lines are returned as skipped, never fatal (the file is
// hand-appended working state).
func LoadReactions(path string) (reactions []Reaction, skipped []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open reactions: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		if len(raw) == 0 {
			continue
		}
		var r Reaction
		if uerr := json.Unmarshal([]byte(raw), &r); uerr != nil {
			skipped = append(skipped, fmt.Sprintf("line %d: %v", line, uerr))
			continue
		}
		reactions = append(reactions, r)
	}
	return reactions, skipped, sc.Err()
}

// Effective reduces raw lines to one effective reaction per real turn_id,
// applying the D-OL2 precedence: any retro line beats all live lines; among
// same-phase lines, file order wins (later = newer — the log is append-only).
// A newest-effective `label:"retracted"` removes the turn. Lines with empty
// or "none" turn_id are unjoinable and counted.
func Effective(reactions []Reaction) (effective map[string]Reaction, unjoinable int) {
	effective = map[string]Reaction{}
	for _, r := range reactions {
		if r.TurnID == "" || r.TurnID == "none" {
			unjoinable++
			continue
		}
		cur, ok := effective[r.TurnID]
		if !ok || r.Phase == "retro" || cur.Phase != "retro" {
			effective[r.TurnID] = r
		}
	}
	for id, r := range effective {
		if r.Label == "retracted" {
			delete(effective, id)
		}
	}
	return effective, unjoinable
}

// LoadTurn reads one ledger record.
func LoadTurn(kbRoot, turnID string) (*shortterm.TurnRecord, error) {
	raw, err := os.ReadFile(filepath.Join(kbRoot, "short-term", "turns", turnID+".yaml"))
	if err != nil {
		return nil, err
	}
	var t shortterm.TurnRecord
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse turn %s: %w", turnID, err)
	}
	return &t, nil
}

// Join resolves each effective reaction to its turn record, in deterministic
// (sorted turn_id) order. Missing turns are joined with Turn=nil.
func Join(effective map[string]Reaction, kbRoot string) []Signal {
	ids := make([]string, 0, len(effective))
	for id := range effective {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Signal, 0, len(ids))
	for _, id := range ids {
		t, err := LoadTurn(kbRoot, id)
		if err != nil {
			t = nil
		}
		out = append(out, Signal{Reaction: effective[id], Turn: t})
	}
	return out
}
