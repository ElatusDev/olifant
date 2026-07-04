package harvest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cursor is the dedup state: the set of turn_ids already proposed (D-HV5).
// Deleting the file re-harvests from scratch.
type Cursor struct {
	Harvested []string `json:"harvested"`
}

// LoadCursor returns an empty cursor when the file is absent.
func LoadCursor(path string) (map[string]bool, error) {
	out := map[string]bool{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse cursor: %w", err)
	}
	for _, id := range c.Harvested {
		out[id] = true
	}
	return out, nil
}

// SaveCursor persists the union of prior + newly proposed turn_ids, sorted
// (deterministic bytes for identical inputs).
func SaveCursor(path string, prior map[string]bool, proposals []Proposal) error {
	set := map[string]bool{}
	for id := range prior {
		set[id] = true
	}
	for _, p := range proposals {
		set[p.TurnID] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	raw, err := json.MarshalIndent(Cursor{Harvested: ids}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// highWaterTS is the max reaction ts across proposals' source signals — used
// to name the report deterministically (no clock reads, D-HV6/resume-safety).
func highWaterTS(signals []Signal) string {
	max := ""
	for _, s := range signals {
		if s.Reaction.TS > max {
			max = s.Reaction.TS
		}
	}
	if max == "" {
		return "empty"
	}
	return strings.NewReplacer(":", "-").Replace(max)
}

// WriteReport renders the human-reviewable proposal report (markdown) under
// outDir, named by the signal high-water timestamp. Propose-only: this file
// and the cursor are harvest's only writes (D-HV1/2).
func WriteReport(outDir string, signals []Signal, proposals []Proposal, unjoinable int, missingTurns []string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# olifant harvest — proposal report\n\n")
	fmt.Fprintf(&b, "> signal high-water: %s · effective signals: %d · unjoinable lines: %d · missing turn records: %d\n",
		highWaterTS(signals), len(signals), unjoinable, len(missingTurns))
	fmt.Fprintf(&b, "> PROPOSE-ONLY (D-HV1/2): nothing below is applied; each item names its gated home.\n\n")

	byKind := map[Kind][]Proposal{}
	for _, p := range proposals {
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}

	section := func(k Kind, title, home string) {
		ps := byKind[k]
		fmt.Fprintf(&b, "## %s (%d)\n\n", title, len(ps))
		if home != "" {
			fmt.Fprintf(&b, "_Accept path: %s_\n\n", home)
		}
		if len(ps) == 0 {
			fmt.Fprintf(&b, "(none)\n\n")
			return
		}
		for _, p := range ps {
			fmt.Fprintf(&b, "### %s\n", p.ID)
			fmt.Fprintf(&b, "- turn: `%s` · subcommand: %s · label: %s\n", p.TurnID, p.Sub, p.Label)
			if len(p.Scope) > 0 {
				fmt.Fprintf(&b, "- scope: %s\n", strings.Join(p.Scope, ","))
			}
			if p.Verdict != "" {
				fmt.Fprintf(&b, "- recorded verdict: %s\n", p.Verdict)
			}
			if p.Request != "" {
				fmt.Fprintf(&b, "- request: %s\n", oneLine(p.Request, 200))
			}
			if len(p.Cites) > 0 {
				fmt.Fprintf(&b, "- cites: %s\n", strings.Join(p.Cites, ", "))
			}
			if p.Evidence != "" {
				fmt.Fprintf(&b, "- evidence: %s\n", oneLine(p.Evidence, 200))
			}
			if k == KindEvalCase {
				fmt.Fprintf(&b, "- suite entry (accept = append to the NEW real-usage suite, never code-feeding-v2 — D-HV4):\n")
				fmt.Fprintf(&b, "  ```yaml\n  - id: %s\n    scope: [%s]\n    request: %s\n  ```\n",
					p.TurnID, strings.Join(p.Scope, ", "), oneLine(p.Request, 300))
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	section(KindEvalCase, "Eval-case candidates (runnable skeletons, accept-only)", "eval/suites/real-usage-v1.yaml via `olifant harvest accept`")
	section(KindEvalPointer, "Eval-case pointers (truncated source — revisit turn)", "manual: re-run + author the case (HV-F2 if recurring)")
	section(KindInvestigate, "Investigate tickets (wrong/partial verdicts)", "GitHub issue on the relevant repo, citing the turn")
	section(KindCorpusGap, "Corpus-gap tickets (unresolved cites)", "KB authoring via the normal gated path")
	section(KindDictTerm, "Dictionary-term candidates", "CNL add-term via challenge (the only legal path)")

	if len(missingTurns) > 0 {
		fmt.Fprintf(&b, "## Missing turn records\n\n")
		for _, id := range missingTurns {
			fmt.Fprintf(&b, "- `%s` — reaction exists, ledger record absent on this host\n", id)
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, "report-"+highWaterTS(signals)+".md")
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
