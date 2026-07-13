package harvest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ElatusDev/olifant/internal/shortterm"
)

// Kind buckets a proposal.
type Kind string

const (
	KindEvalCase    Kind = "eval-case"    // accept-only, runnable skeleton (challenge family)
	KindEvalPointer Kind = "eval-pointer" // accept-only, non-runnable (truncated request — IA5)
	KindInvestigate Kind = "investigate"  // reject/wrong or partial — a verdict that misled
	KindCorpusGap   Kind = "corpus-gap"   // unresolved cites → KB authoring/finding ticket
	KindDictTerm    Kind = "dict-term"    // plain-word unresolved cite → CNL term candidate
)

// Proposal is one human-reviewable item. Nothing here is ever auto-applied.
type Proposal struct {
	ID       string   `yaml:"id"`
	Kind     Kind     `yaml:"kind"`
	TurnID   string   `yaml:"turn_id"`
	Sub      string   `yaml:"subcommand"`
	Scope    []string `yaml:"scope,omitempty"`
	Request  string   `yaml:"request,omitempty"`
	Claim    string   `yaml:"claim,omitempty"` // validate surface (olifant#86): full claim
	Diff     string   `yaml:"diff,omitempty"`  // validate surface: frozen diff snapshot (D-VC3)
	Verdict  string   `yaml:"verdict,omitempty"`
	Label    string   `yaml:"label,omitempty"`
	Evidence string   `yaml:"evidence,omitempty"`
	Cites    []string `yaml:"cites,omitempty"` // for validate: seeds the expected must_cite_any_of skeleton (D-VC5)
}

// runnableSubcommands are those whose ledger `request` is the full input —
// a case skeleton can re-run them. `validate` is runnable via a different
// route (olifant#86): its display `request` is truncated, but the enriched
// ValidateBlock carries the full claim + frozen diff, so a validate turn is
// reconstructed from the block, not the request (see validateRunnable).
var runnableSubcommands = map[string]bool{
	"challenge": true, "prompt context": true, "prompt build": true,
}

// truncatedRE detects the ledger's request-cap ellipsis.
var truncatedRE = regexp.MustCompile(`…`)

// plainWordRE: a dictionary-term candidate is a bare lowercase word/phrase —
// not a path, not an artifact ID.
var plainWordRE = regexp.MustCompile(`^[a-z][a-z0-9 _-]{2,40}$`)

// Classify maps signals to proposals with pure rules (D-HV6). Signals whose
// turn_id is in harvested are skipped (D-HV5 cursor dedup).
func Classify(signals []Signal, harvested map[string]bool) []Proposal {
	var out []Proposal
	add := func(p Proposal) {
		p.ID = string(p.Kind) + ":" + p.TurnID
		out = append(out, p)
	}
	for _, s := range signals {
		r := s.Reaction
		if harvested[r.TurnID] {
			continue
		}
		var scope []string
		request, verdict := "", r.Verdict
		if s.Turn != nil {
			scope, request = s.Turn.Scope, s.Turn.Request
			if verdict == "" {
				verdict = turnVerdict(s.Turn)
			}
		}

		label := r.Label
		if label == "" {
			label = map[string]string{"accept": "confirmed", "reject": "wrong", "partial": "partial"}[r.Reaction]
		}

		switch label {
		case "confirmed":
			// A validate turn reconstructs from the enriched block (full claim +
			// frozen diff), not the truncated request (olifant#86). A pre-#86
			// thin block (empty Claim) has nothing to reconstruct → pointer.
			if vb := validateRunnable(s.Turn); vb != nil {
				add(Proposal{Kind: KindEvalCase, TurnID: r.TurnID, Sub: r.Subcommand,
					Scope: scope, Claim: vb.Claim, Diff: vb.Diff, Verdict: vbVerdict(verdict, vb),
					Cites: vb.Cites, Label: label, Evidence: r.Note})
				break
			}
			kind := KindEvalPointer
			if runnableSubcommands[r.Subcommand] && s.Turn != nil && !truncatedRE.MatchString(request) {
				kind = KindEvalCase
			}
			add(Proposal{Kind: kind, TurnID: r.TurnID, Sub: r.Subcommand,
				Scope: scope, Request: request, Verdict: verdict, Label: label, Evidence: r.Note})
		case "wrong", "partial":
			add(Proposal{Kind: KindInvestigate, TurnID: r.TurnID, Sub: r.Subcommand,
				Scope: scope, Request: request, Verdict: verdict, Label: label, Evidence: r.Note})
		}

		if len(r.UnresolvedCites) > 0 {
			var gaps, terms []string
			for _, c := range r.UnresolvedCites {
				if plainWordRE.MatchString(strings.TrimSpace(c)) {
					terms = append(terms, strings.TrimSpace(c))
				} else {
					gaps = append(gaps, c)
				}
			}
			sort.Strings(gaps)
			sort.Strings(terms)
			if len(gaps) > 0 {
				add(Proposal{Kind: KindCorpusGap, TurnID: r.TurnID, Sub: r.Subcommand,
					Cites: gaps, Label: label, Evidence: r.Note})
			}
			if len(terms) > 0 {
				add(Proposal{Kind: KindDictTerm, TurnID: r.TurnID, Sub: r.Subcommand,
					Cites: terms, Label: label, Evidence: r.Note})
			}
		}
	}
	return out
}

// CaseYAML renders an accepted eval-case proposal as a suite entry. A validate
// proposal (olifant#86) emits claim + frozen diff + an `expected:` skeleton
// (verdict + must_cite_any_of from the run's actuals — a DRAFT the human
// confirms/trims, D-VC5); a challenge proposal emits the request form.
func CaseYAML(p Proposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - id: %s\n    scope: [%s]\n", p.TurnID, strings.Join(p.Scope, ", "))
	if p.Claim == "" {
		fmt.Fprintf(&b, "    request: %q\n", p.Request)
		return b.String()
	}
	fmt.Fprintf(&b, "    claim: %q\n", p.Claim)
	b.WriteString("    diff: |\n")
	for _, line := range strings.Split(strings.TrimRight(p.Diff, "\n"), "\n") {
		fmt.Fprintf(&b, "      %s\n", line)
	}
	b.WriteString("    expected:   # DRAFT skeleton — confirm/trim before relying on it (D-VC5)\n")
	if p.Verdict != "" {
		fmt.Fprintf(&b, "      verdict: %s\n", p.Verdict)
	}
	if len(p.Cites) > 0 {
		fmt.Fprintf(&b, "      must_cite_any_of: [%s]\n", strings.Join(p.Cites, ", "))
	}
	return b.String()
}

// validateRunnable returns the enriched ValidateBlock when a turn is a
// reconstructable validate case — i.e. the block carries the full claim +
// frozen diff (olifant#86). A pre-#86 thin block (no Claim/Diff) returns nil,
// so it degrades to a pointer, never a broken case.
func validateRunnable(t *shortterm.TurnRecord) *shortterm.ValidateBlock {
	if t != nil && t.Validate != nil && t.Validate.Claim != "" && t.Validate.Diff != "" {
		return t.Validate
	}
	return nil
}

// vbVerdict prefers the reaction/turn verdict, falling back to the block's.
func vbVerdict(v string, vb *shortterm.ValidateBlock) string {
	if v != "" {
		return v
	}
	return vb.Verdict
}

// turnVerdict extracts the block verdict when the reaction line lacks one.
func turnVerdict(t *shortterm.TurnRecord) string {
	switch {
	case t.Challenge != nil:
		return t.Challenge.Verdict
	case t.Validate != nil:
		return t.Validate.Verdict
	case t.PromptCheck != nil:
		return t.PromptCheck.Verdict
	default:
		return ""
	}
}
