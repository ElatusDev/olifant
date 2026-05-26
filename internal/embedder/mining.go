package embedder

import (
	"fmt"
	"math"
	"sort"
)

// Triple is one training row: anchor sentence + corpus-mined hard negative.
// The positive paraphrase is filled by the Opus generator stage (gen.go).
type Triple struct {
	AnchorID     string
	Anchor       string
	NegativeID   string
	Negative     string
	Scope        string
	AnchorRole   string
	NegativeRole string
	SourcePath   string
	Relaxed      bool // true if same-scope+different-role pool was empty and we broadened
}

// vocab indexes every distinct tag value (concern, language, syntactic_form)
// into a dense int. Used to build per-sentence multi-hot vectors for cosine.
type vocab struct {
	idx map[string]int
}

func newVocab(sentences []Sentence) *vocab {
	v := &vocab{idx: map[string]int{}}
	for _, s := range sentences {
		for _, c := range s.Concerns {
			v.add("c:" + c)
		}
		if s.Language != "" {
			v.add("l:" + s.Language)
		}
		if s.Tags.SyntacticForm != "" {
			v.add("f:" + s.Tags.SyntacticForm)
		}
	}
	return v
}

func (v *vocab) add(k string) {
	if _, ok := v.idx[k]; !ok {
		v.idx[k] = len(v.idx)
	}
}

func (v *vocab) vec(s Sentence) []bool {
	out := make([]bool, len(v.idx))
	for _, c := range s.Concerns {
		if i, ok := v.idx["c:"+c]; ok {
			out[i] = true
		}
	}
	if i, ok := v.idx["l:"+s.Language]; ok && s.Language != "" {
		out[i] = true
	}
	if i, ok := v.idx["f:"+s.Tags.SyntacticForm]; ok && s.Tags.SyntacticForm != "" {
		out[i] = true
	}
	return out
}

func cosine(a, b []bool) float64 {
	dot, na, nb := 0, 0, 0
	for i := range a {
		if a[i] {
			na++
			if b[i] {
				dot++
			}
		}
		if b[i] {
			nb++
		}
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float64(dot) / math.Sqrt(float64(na)*float64(nb))
}

// Mine produces a Triple per eligible anchor. For each anchor it picks the
// hard negative as the most-cosine-similar sentence in the same scope with
// a different semantic_role. Ties break by lexicographic ID for determinism.
//
// If no same-scope+different-role candidate exists, we broaden to any-scope
// +different-role and set Relaxed=true on the resulting Triple so downstream
// can filter if desired.
//
// Anchors with no valid candidate at all (i.e., the entire corpus shares the
// anchor's role) are dropped.
func Mine(sentences []Sentence) []Triple {
	if len(sentences) == 0 {
		return nil
	}
	v := newVocab(sentences)
	vecs := make([][]bool, len(sentences))
	for i := range sentences {
		vecs[i] = v.vec(sentences[i])
	}
	byScope := ScopeIndex(sentences)

	triples := make([]Triple, 0, len(sentences))
	for i, anchor := range sentences {
		// Step 1: same scope, different role.
		bestIdx, bestScore, bestID, relaxed := -1, -1.0, "", false
		for _, j := range byScope[anchor.Scope] {
			if j == i {
				continue
			}
			cand := sentences[j]
			if cand.SemanticRole == anchor.SemanticRole {
				continue
			}
			score := cosine(vecs[i], vecs[j])
			if betterPick(score, bestScore, cand.ID, bestID) {
				bestIdx, bestScore, bestID = j, score, cand.ID
			}
		}

		// Step 2: broaden if empty.
		if bestIdx < 0 {
			for j, cand := range sentences {
				if j == i {
					continue
				}
				if cand.SemanticRole == anchor.SemanticRole {
					continue
				}
				score := cosine(vecs[i], vecs[j])
				if betterPick(score, bestScore, cand.ID, bestID) {
					bestIdx, bestScore, bestID = j, score, cand.ID
				}
			}
			relaxed = bestIdx >= 0
		}

		if bestIdx < 0 {
			continue
		}
		neg := sentences[bestIdx]
		triples = append(triples, Triple{
			AnchorID:     anchor.ID,
			Anchor:       anchor.Text,
			NegativeID:   neg.ID,
			Negative:     neg.Text,
			Scope:        anchor.Scope,
			AnchorRole:   anchor.SemanticRole,
			NegativeRole: neg.SemanticRole,
			SourcePath:   anchor.Source,
			Relaxed:      relaxed,
		})
	}
	return triples
}

// betterPick prefers higher cosine; on tie, lex-smaller candidate ID for
// determinism. `bestID` is the current best (or "" if none yet).
func betterPick(score, bestScore float64, candID, bestID string) bool {
	if bestID == "" {
		return true
	}
	if score > bestScore {
		return true
	}
	if score == bestScore && candID < bestID {
		return true
	}
	return false
}

// MiningStats summarises a Mine() call's distribution. Useful for the
// `embedder-triples` subcommand's progress + sanity output.
type MiningStats struct {
	AnchorCount   int
	TripleCount   int
	RelaxedCount  int
	DroppedCount  int
	ByScope       map[string]int
	ByRole        map[string]int
	CrossScopeIDs []string // IDs of relaxed triples for spot-check
}

// Summarise builds a MiningStats over the given anchors + triples.
func Summarise(sentences []Sentence, triples []Triple) MiningStats {
	st := MiningStats{
		AnchorCount: len(sentences),
		TripleCount: len(triples),
		ByScope:     map[string]int{},
		ByRole:      map[string]int{},
	}
	for _, t := range triples {
		st.ByScope[t.Scope]++
		st.ByRole[t.AnchorRole]++
		if t.Relaxed {
			st.RelaxedCount++
			if len(st.CrossScopeIDs) < 10 {
				st.CrossScopeIDs = append(st.CrossScopeIDs, t.AnchorID)
			}
		}
	}
	st.DroppedCount = st.AnchorCount - st.TripleCount
	return st
}

// HumanString returns a one-screen summary suitable for verbose CLI output.
func (st MiningStats) HumanString() string {
	out := fmt.Sprintf("anchors=%d  triples=%d  dropped=%d  relaxed=%d\n",
		st.AnchorCount, st.TripleCount, st.DroppedCount, st.RelaxedCount)
	out += "  per-scope:\n"
	scopes := make([]string, 0, len(st.ByScope))
	for s := range st.ByScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	for _, s := range scopes {
		out += fmt.Sprintf("    %-20s %d\n", s, st.ByScope[s])
	}
	return out
}
