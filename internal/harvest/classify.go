package harvest

import (
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
	Verdict  string   `yaml:"verdict,omitempty"`
	Label    string   `yaml:"label,omitempty"`
	Evidence string   `yaml:"evidence,omitempty"`
	Cites    []string `yaml:"cites,omitempty"`
}

// runnableSubcommands are those whose ledger `request` is the full input —
// a case skeleton can re-run them. validate requests are truncated (IA4/IA5)
// and become pointers.
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
