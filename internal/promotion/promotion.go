// Package promotion is the charter §7 promotion-state ledger: the single
// durable source of truth for whether each model-judgment verdict surface
// (challenge, validate) currently BLOCKS on a hard verdict or only annotates
// (advisory). The enforcement in cmd/{challenge,validate}.go reads it; an
// absent or empty ledger means every surface is advisory (fail-safe default),
// so a fresh machine never blocks until an operator promotes.
//
// A promotion must name its decision + receipt evidence (charter §7 criterion
// 4 — enforced by Promote); Demote flips a surface back to advisory in one op,
// the wired demotion path (olifant#87 AC4). Auto-detecting the false-block
// trigger from reaction labels is deferred (VP-F1). The ledger is the machine
// twin of the human-readable "Promotions executed" register in CHARTER.md §7.
package promotion

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Surface names — the two model-judgment verdict surfaces §7 can promote.
const (
	SurfaceChallenge = "challenge"
	SurfaceValidate  = "validate"
)

// Status values.
const (
	StatusAdvisory = "advisory"
	StatusBlocking = "blocking"
)

// Surfaces lists the promotable verdict surfaces in stable order.
var Surfaces = []string{SurfaceChallenge, SurfaceValidate}

// hardBlockProceed maps a surface to the single CNL `proceed` value that
// constitutes a hard block for it. challenge and validate use different proceed
// enums, so the block predicate is per-surface; only these two values block —
// the soft verdicts (challenge confirm_with_user, validate hold) and the clear
// ones (proceed_directly, merge) never do (olifant#87 D-VP1).
var hardBlockProceed = map[string]string{
	SurfaceChallenge: "abort", // verdict INVALID / OUT_OF_SCOPE
	SurfaceValidate:  "block", // overall_verdict failed
}

// Known reports whether name is a promotable surface.
func Known(surface string) bool {
	_, ok := hardBlockProceed[surface]
	return ok
}

// State is the on-disk ledger. The zero value (or an absent file) means every
// surface is advisory.
type State struct {
	Surfaces map[string]*Surface `yaml:"surfaces,omitempty"`
}

// Surface is one verdict surface's promotion record.
type Surface struct {
	Status     string    `yaml:"status"`             // advisory | blocking
	Decision   string    `yaml:"decision,omitempty"` // the §7 decision, e.g. D250
	Receipts   []string  `yaml:"receipts,omitempty"` // streak run_ids — the evidence
	PromotedAt string    `yaml:"promoted_at,omitempty"`
	Demotion   *Demotion `yaml:"demotion,omitempty"` // last demotion, if any
}

// Demotion records a flip back to advisory (charter §7 demotion clause).
type Demotion struct {
	At     string `yaml:"at"`
	Reason string `yaml:"reason"`
}

// DefaultPath is ~/.olifant/promotion-state.yaml — the user-local operational
// twin of the CHARTER.md §7 register (mirrors the receipts / lns convention).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".olifant", "promotion-state.yaml"), nil
}

// Load reads the ledger at path. A missing file is not an error: it returns an
// empty State (every surface advisory) so enforcement fails safe.
func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("promotion: parse %s: %w", path, err)
	}
	return &s, nil
}

// Save writes the ledger atomically (temp + rename), creating parent dirs.
func Save(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsBlocking reports whether surface is currently promoted to blocking. An
// unknown surface or empty state => false (advisory default).
func (s *State) IsBlocking(surface string) bool {
	if s == nil || s.Surfaces == nil {
		return false
	}
	sf := s.Surfaces[surface]
	return sf != nil && sf.Status == StatusBlocking
}

// HardBlocks reports whether a live verdict with the given proceed value must
// block: the surface is promoted to blocking AND proceed is that surface's
// hard-block value. Soft/clear proceed values (and an unparsed empty proceed)
// never block (olifant#87 D-VP1, fail-safe).
func (s *State) HardBlocks(surface, proceed string) bool {
	if !s.IsBlocking(surface) {
		return false
	}
	hb, ok := hardBlockProceed[surface]
	return ok && proceed != "" && proceed == hb
}

// Promote marks surface blocking, recording the §7 decision + receipt evidence.
// It errors on an unknown surface, a blank decision, or fewer than two receipts
// — the two-consecutive-PASS bar (charter §7 criteria 2 + 4 by construction).
func Promote(path, surface, decision string, receipts []string, now string) error {
	if !Known(surface) {
		return fmt.Errorf("promotion: unknown surface %q", surface)
	}
	if decision == "" {
		return fmt.Errorf("promotion: a promotion must name its decision (charter §7 criterion 4)")
	}
	if len(receipts) < 2 {
		return fmt.Errorf("promotion: need >=2 receipt ids (two-consecutive-PASS streak); got %d", len(receipts))
	}
	s, err := Load(path)
	if err != nil {
		return err
	}
	if s.Surfaces == nil {
		s.Surfaces = map[string]*Surface{}
	}
	s.Surfaces[surface] = &Surface{
		Status:     StatusBlocking,
		Decision:   decision,
		Receipts:   receipts,
		PromotedAt: now,
	}
	return Save(path, s)
}

// Demote flips surface back to advisory, recording the reason + time — the
// wired §7 demotion path (olifant#87 AC4). Idempotent: demoting an already
// advisory (or absent) surface records the demotion without error.
func Demote(path, surface, reason, now string) error {
	if !Known(surface) {
		return fmt.Errorf("promotion: unknown surface %q", surface)
	}
	if reason == "" {
		return fmt.Errorf("promotion: demotion must name a reason (the confirmed false block)")
	}
	s, err := Load(path)
	if err != nil {
		return err
	}
	if s.Surfaces == nil {
		s.Surfaces = map[string]*Surface{}
	}
	sf := s.Surfaces[surface]
	if sf == nil {
		sf = &Surface{}
		s.Surfaces[surface] = sf
	}
	sf.Status = StatusAdvisory
	sf.Demotion = &Demotion{At: now, Reason: reason}
	return Save(path, s)
}

// StatusOf returns surface's current status, defaulting to advisory for an
// unknown or unrecorded surface.
func (s *State) StatusOf(surface string) string {
	if s != nil && s.Surfaces != nil {
		if sf := s.Surfaces[surface]; sf != nil && sf.Status != "" {
			return sf.Status
		}
	}
	return StatusAdvisory
}
