// Phase B1c — held-out recall@5 evaluation of the trained domain
// embedder (candidate) against nomic-embed-text (baseline), per
// olifant-rag-phase-b-prompt.md §4 B1c. The candidate's embeddings are
// produced server-side on Modal (modal_app.py::recall_embed) because
// `modal volume get` of the model artefact is blocked locally; this
// file owns the suite format, ranking, recall math, and the report.
package embedder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// GateRelativeImprovement is the GB1 user-gate threshold: the candidate
// must improve recall@5 by ≥10% relative over the baseline.
const GateRelativeImprovement = 0.10

// RecallK is the rank cutoff for the headline metric.
const RecallK = 5

// Query is one entry in the recall suite YAML.
type Query struct {
	ID             string `yaml:"id" json:"id"`
	Scope          string `yaml:"scope" json:"scope"`
	Text           string `yaml:"text" json:"text"`
	ExpectedSource string `yaml:"expected_source" json:"expected_source"`
}

// RecallSuite is the on-disk suite shape (eval/recall/queries-v1.yaml).
type RecallSuite struct {
	SuiteID string  `yaml:"suite_id"`
	Queries []Query `yaml:"queries"`
}

// LoadRecallSuite parses and validates the suite: non-empty, unique IDs,
// and every query carrying text + expected_source.
func LoadRecallSuite(path string) (*RecallSuite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s RecallSuite
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(s.Queries) == 0 {
		return nil, fmt.Errorf("%s: no queries", path)
	}
	seen := map[string]bool{}
	for i, q := range s.Queries {
		if q.ID == "" || q.Text == "" || q.ExpectedSource == "" {
			return nil, fmt.Errorf("%s: query #%d (%q) missing id/text/expected_source", path, i+1, q.ID)
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("%s: duplicate query id %q", path, q.ID)
		}
		seen[q.ID] = true
	}
	return &s, nil
}

// Hit is one retrieved sentence for a query.
type Hit struct {
	SentenceID string  `json:"sentence_id"`
	Source     string  `json:"source"`
	Score      float64 `json:"score"`
}

// TopK ranks sentences by cosine similarity to the query vector and
// returns the k best. sentVecs[i] corresponds to sents[i].
func TopK(query []float32, sentVecs [][]float32, sents []Sentence, k int) []Hit {
	type scored struct {
		idx   int
		score float64
	}
	all := make([]scored, 0, len(sentVecs))
	for i, v := range sentVecs {
		all = append(all, scored{i, Cosine(query, v)})
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	if k > len(all) {
		k = len(all)
	}
	hits := make([]Hit, 0, k)
	for _, s := range all[:k] {
		hits = append(hits, Hit{
			SentenceID: sents[s.idx].ID,
			Source:     sents[s.idx].Source,
			Score:      s.score,
		})
	}
	return hits
}

// Cosine returns the cosine similarity of two vectors; 0 for mismatched
// or zero-norm inputs.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// QueryResult is one query's outcome for one embedder.
type QueryResult struct {
	QueryID        string `json:"query_id"`
	ExpectedSource string `json:"expected_source"`
	Hits           []Hit  `json:"hits"`
	// HitAt is the 1-based rank of the first hit whose source matches
	// ExpectedSource; 0 means a miss within the recorded hits.
	HitAt int `json:"hit_at"`
}

// ScoreResults stamps HitAt on each result from its hits.
func ScoreResults(results []QueryResult) {
	for i := range results {
		results[i].HitAt = 0
		for r, h := range results[i].Hits {
			if h.Source == results[i].ExpectedSource {
				results[i].HitAt = r + 1
				break
			}
		}
	}
}

// RecallAt returns the fraction of results with 1 <= HitAt <= k.
func RecallAt(results []QueryResult, k int) float64 {
	if len(results) == 0 {
		return 0
	}
	n := 0
	for _, r := range results {
		if r.HitAt >= 1 && r.HitAt <= k {
			n++
		}
	}
	return float64(n) / float64(len(results))
}

// EmbedderRecall is one embedder's side of the comparison.
type EmbedderRecall struct {
	Name    string        `json:"name"`
	Recall5 float64       `json:"recall_at_5"`
	Results []QueryResult `json:"results"`
}

// RecallReport is the persisted comparison artefact (GB1 evidence).
type RecallReport struct {
	SuiteID   string         `json:"suite_id"`
	Queries   int            `json:"queries"`
	Baseline  EmbedderRecall `json:"baseline"`
	Candidate EmbedderRecall `json:"candidate"`
	// RelativeImprovement is (candidate - baseline) / baseline; +Inf is
	// encoded as the sentinel value below when baseline recall is 0.
	RelativeImprovement float64 `json:"relative_improvement"`
	GatePass            bool    `json:"gate_pass"`
	GateThreshold       float64 `json:"gate_threshold"`
}

// BuildReport assembles the comparison and evaluates gate GB1. A zero
// baseline with a positive candidate passes (infinite relative
// improvement, encoded as math.MaxFloat64); zero on both sides fails —
// per the B1c retry policy that suite is biased and must be rewritten.
func BuildReport(suiteID string, baseline, candidate EmbedderRecall) RecallReport {
	rep := RecallReport{
		SuiteID:       suiteID,
		Queries:       len(baseline.Results),
		Baseline:      baseline,
		Candidate:     candidate,
		GateThreshold: GateRelativeImprovement,
	}
	switch {
	case baseline.Recall5 == 0 && candidate.Recall5 == 0:
		rep.RelativeImprovement = 0
		rep.GatePass = false
	case baseline.Recall5 == 0:
		rep.RelativeImprovement = math.MaxFloat64
		rep.GatePass = true
	default:
		rep.RelativeImprovement = (candidate.Recall5 - baseline.Recall5) / baseline.Recall5
		rep.GatePass = rep.RelativeImprovement >= GateRelativeImprovement
	}
	return rep
}

// remoteRecallPayload is what modal_app.py::recall_embed prints between
// the stdout markers: per-query candidate hits, ranked server-side.
type remoteRecallPayload struct {
	Queries []struct {
		QueryID string `json:"query_id"`
		Hits    []Hit  `json:"hits"`
	} `json:"queries"`
}

const (
	recallJSONBegin = "===OLIFANT_RECALL_JSON==="
	recallJSONEnd   = "===END_OLIFANT_RECALL_JSON==="
)

// ParseRemoteRecall extracts the marker-delimited JSON payload from the
// `modal run` stdout and maps it onto QueryResults in suite order.
func ParseRemoteRecall(stdout []byte, queries []Query) ([]QueryResult, error) {
	begin := bytes.Index(stdout, []byte(recallJSONBegin))
	end := bytes.Index(stdout, []byte(recallJSONEnd))
	if begin < 0 || end < 0 || end < begin {
		return nil, fmt.Errorf("recall markers not found in modal output (%d bytes)", len(stdout))
	}
	var payload remoteRecallPayload
	raw := stdout[begin+len(recallJSONBegin) : end]
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return nil, fmt.Errorf("parse remote recall payload: %w", err)
	}
	byID := map[string][]Hit{}
	for _, q := range payload.Queries {
		byID[q.QueryID] = q.Hits
	}
	results := make([]QueryResult, 0, len(queries))
	for _, q := range queries {
		hits, ok := byID[q.ID]
		if !ok {
			return nil, fmt.Errorf("remote payload missing query %q", q.ID)
		}
		results = append(results, QueryResult{
			QueryID:        q.ID,
			ExpectedSource: q.ExpectedSource,
			Hits:           hits,
		})
	}
	ScoreResults(results)
	return results, nil
}

// WriteRecallInputs emits the two JSONL files recall_embed reads from
// the Modal volume: sentences (id, text, source) and queries (id, text).
func WriteRecallInputs(sentencesPath, queriesPath string, sents []Sentence, queries []Query) error {
	sf, err := os.Create(sentencesPath)
	if err != nil {
		return err
	}
	defer sf.Close()
	senc := json.NewEncoder(sf)
	for _, s := range sents {
		row := map[string]string{"id": s.ID, "text": s.Text, "source": s.Source}
		if err := senc.Encode(row); err != nil {
			return err
		}
	}

	qf, err := os.Create(queriesPath)
	if err != nil {
		return err
	}
	defer qf.Close()
	qenc := json.NewEncoder(qf)
	for _, q := range queries {
		row := map[string]string{"id": q.ID, "text": q.Text}
		if err := qenc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}
