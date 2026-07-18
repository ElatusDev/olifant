// reconcile.go wires the charter §7 demotion TRIGGER (VP-F1, olifant#93):
// a deterministic, offline scan of the human reaction side-log that invokes
// the existing Demote when a promoted surface produced a confirmed false
// block. D250 wired the flip; this is what makes the clause's "automatic"
// true. It acts ONLY on human-authored labels (live `reject`, retro `wrong` —
// the D213 channel; never a raw model verdict) and only demotes on a signal
// newer than the surface's PromotedAt, so re-promotion is safe against the
// append-only log with no cursor file. Ambiguity keeps enforcement: a signal
// that cannot be fully read (unparseable ts, absent verdict) is skipped and
// reported, never acted on — the inverse fail-safe of the ledger's
// absent⇒advisory, for the same reason.
package promotion

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/harvest"
)

// hardBlockVerdicts maps a surface to the verdict strings that constitute its
// hard block — the verdict-level twin of hardBlockProceed, used when a
// reaction line carries a verdict but its turn record (and thus `proceed`)
// is unjoinable, which the live side-log shows is common.
var hardBlockVerdicts = map[string]map[string]bool{
	SurfaceChallenge: {"INVALID": true, "OUT_OF_SCOPE": true}, // proceed=abort
	SurfaceValidate:  {"failed": true},                        // proceed=block
}

// Disposition actions.
const (
	ActionDemoted     = "demoted"
	ActionWouldDemote = "would-demote"
	ActionSkipped     = "skipped"
)

// ReconcileConfig are the inputs — no clock reads inside (Now is injected,
// mirroring Promote/Demote) and no network, so a reconcile is a pure function
// of the two files.
type ReconcileConfig struct {
	ReactionsPath string
	StatePath     string
	KBRoot        string // optional: enables the turn join for `proceed`; empty = verdict-map only
	DryRun        bool
	Now           string
}

// Disposition is one signal's outcome — every signal gets one, so -dry-run is
// a complete audit view (nothing is silently dropped).
type Disposition struct {
	Surface string
	TurnID  string
	TS      string
	Verdict string
	Action  string
	Reason  string
}

// ReconcileReport is the full account of one reconcile pass, in side-log
// line order (deterministic).
type ReconcileReport struct {
	Demoted         []Disposition
	Skipped         []Disposition
	Malformed       []string
	ReactionsAbsent bool
}

// tsLayouts are the timestamp shapes observed in the live side-log: RFC3339
// and bare dates (a date-only ts compares at midnight UTC).
var tsLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"}

func parseTS(s string) (time.Time, bool) {
	for _, layout := range tsLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// realTurnID reports whether id names an actual ledger record. The live log
// uses "unknown"/"none-*" placeholders freely; those lines are evaluated
// per-line (a by-id reduction would collide them — spike F3).
func realTurnID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "none") && !strings.HasPrefix(id, "unknown")
}

// liveLabel mirrors the harvest classify mapping (reject → wrong etc.) so the
// live and retro channels share one vocabulary.
var liveLabel = map[string]string{"accept": "confirmed", "reject": "wrong", "partial": "partial"}

// Reconcile scans the reaction side-log and demotes any promoted surface with
// a confirmed false block newer than its promotion (charter §7 demotion
// clause). An absent side-log is a clean no-op; an unreadable state file is an
// error (never guess past unreadable state).
func Reconcile(cfg ReconcileConfig) (*ReconcileReport, error) {
	rep := &ReconcileReport{}
	reactions, malformed, err := harvest.LoadReactions(cfg.ReactionsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rep.ReactionsAbsent = true
			return rep, nil
		}
		return nil, err
	}
	rep.Malformed = malformed

	state, err := Load(cfg.StatePath)
	if err != nil {
		return nil, err
	}

	effective, vetoed := reduceClusters(reactions)
	demotedNow := map[string]bool{}

	for i, r := range reactions {
		switch {
		case vetoed[r.TurnID]:
			rep.skip(r, "", "retracted by a retro line for turn "+r.TurnID)
			continue
		case realTurnID(r.TurnID) && effective[r.TurnID] != i:
			rep.skip(r, "", "superseded by a newer line for turn "+r.TurnID)
			continue
		}
		rep.evaluate(r, cfg, state, demotedNow)
	}
	return rep, nil
}

// reduceClusters applies the D-OL2 precedence within clusters sharing a real
// turn_id: a retro line beats all live lines; among same-standing lines the
// later (newer, append-only) wins. A winning `retracted` label vetoes the
// whole cluster. Returns the winning index per real turn_id.
func reduceClusters(reactions []harvest.Reaction) (effective map[string]int, vetoed map[string]bool) {
	effective = map[string]int{}
	vetoed = map[string]bool{}
	for i, r := range reactions {
		if !realTurnID(r.TurnID) {
			continue
		}
		cur, seen := effective[r.TurnID]
		if !seen || r.Phase == "retro" || reactions[cur].Phase != "retro" {
			effective[r.TurnID] = i
		}
	}
	for id, i := range effective {
		if reactions[i].Label == "retracted" {
			vetoed[id] = true
			delete(effective, id)
		}
	}
	return effective, vetoed
}

// evaluate applies the D-DT2 predicate to one effective signal.
func (rep *ReconcileReport) evaluate(r harvest.Reaction, cfg ReconcileConfig, state *State, demotedNow map[string]bool) {
	surface := r.Subcommand
	if !Known(surface) {
		rep.skip(r, "", "not a promotable verdict surface")
		return
	}
	label := r.Label
	if label == "" {
		label = liveLabel[r.Reaction]
	}
	if label == "" {
		rep.skip(r, "", fmt.Sprintf("no human label (reaction=%q)", r.Reaction))
		return
	}
	if label != "wrong" {
		rep.skip(r, "", "label="+label+" — not a confirmed false block")
		return
	}

	verdict, proceed := r.Verdict, ""
	if cfg.KBRoot != "" && realTurnID(r.TurnID) {
		if t, terr := harvest.LoadTurn(cfg.KBRoot, r.TurnID); terr == nil {
			switch {
			case t.Challenge != nil:
				proceed = t.Challenge.Proceed
				if verdict == "" {
					verdict = t.Challenge.Verdict
				}
			case t.Validate != nil:
				proceed = t.Validate.Proceed
				if verdict == "" {
					verdict = t.Validate.Verdict
				}
			}
		}
	}
	hard := false
	switch {
	case proceed != "":
		hard = proceed == hardBlockProceed[surface]
	case verdict != "":
		hard = hardBlockVerdicts[surface][verdict]
	default:
		rep.skip(r, verdict, "no verdict evidence — keeping enforcement")
		return
	}
	if !hard {
		rep.skip(r, verdict, fmt.Sprintf("verdict %q is not the hard block for %s", verdict, surface))
		return
	}

	if demotedNow[surface] {
		rep.skip(r, verdict, "surface already demoted by an earlier signal this run")
		return
	}
	if !state.IsBlocking(surface) {
		rep.skip(r, verdict, "surface is advisory — nothing to demote")
		return
	}

	promotedAt := state.Surfaces[surface].PromotedAt
	pt, ok := parseTS(promotedAt)
	if !ok {
		rep.skip(r, verdict, fmt.Sprintf("unparseable promoted_at %q — keeping enforcement", promotedAt))
		return
	}
	st, ok := parseTS(r.TS)
	if !ok {
		rep.skip(r, verdict, fmt.Sprintf("unparseable ts %q — keeping enforcement", r.TS))
		return
	}
	if !st.After(pt) {
		rep.skip(r, verdict, fmt.Sprintf("signal ts %s predates promotion %s", r.TS, promotedAt))
		return
	}

	reason := fmt.Sprintf("auto: confirmed false block — %s verdict=%s%s", r.TS, verdict, noteExcerpt(r.Note))
	d := Disposition{Surface: surface, TurnID: r.TurnID, TS: r.TS, Verdict: verdict, Reason: reason}
	if cfg.DryRun {
		d.Action = ActionWouldDemote
	} else {
		if err := Demote(cfg.StatePath, surface, reason, cfg.Now); err != nil {
			rep.skip(r, verdict, "demote failed: "+err.Error())
			return
		}
		d.Action = ActionDemoted
	}
	demotedNow[surface] = true
	rep.Demoted = append(rep.Demoted, d)
}

func (rep *ReconcileReport) skip(r harvest.Reaction, verdict, reason string) {
	if verdict == "" {
		verdict = r.Verdict
	}
	rep.Skipped = append(rep.Skipped, Disposition{
		Surface: r.Subcommand, TurnID: r.TurnID, TS: r.TS,
		Verdict: verdict, Action: ActionSkipped, Reason: reason,
	})
}

// noteExcerpt renders a bounded, single-line fragment of the human note for
// the demotion reason (the ledger must name its confirmed false block).
func noteExcerpt(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		return ""
	}
	if r := []rune(n); len(r) > 80 {
		n = string(r[:80]) + "…"
	}
	return " — " + n
}
