package embedder

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSuite(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "queries.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRecallSuite_HappyPath(t *testing.T) {
	p := writeSuite(t, `
suite_id: s1
queries:
  - id: q01
    scope: backend
    text: "where is the tenant filter?"
    expected_source: patterns/backend.md
  - id: q02
    scope: webapp
    text: "rtk query cache rules?"
    expected_source: patterns/frontend.md
`)
	s, err := LoadRecallSuite(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SuiteID != "s1" || len(s.Queries) != 2 {
		t.Fatalf("suite = %+v", s)
	}
	if s.Queries[0].ExpectedSource != "patterns/backend.md" {
		t.Errorf("q01 expected_source = %q", s.Queries[0].ExpectedSource)
	}
}

func TestLoadRecallSuite_RejectsDuplicateAndIncomplete(t *testing.T) {
	dup := writeSuite(t, `
suite_id: s1
queries:
  - {id: q01, text: "a", expected_source: x.md}
  - {id: q01, text: "b", expected_source: y.md}
`)
	if _, err := LoadRecallSuite(dup); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate id not rejected: %v", err)
	}

	missing := writeSuite(t, `
suite_id: s1
queries:
  - {id: q01, text: "a"}
`)
	if _, err := LoadRecallSuite(missing); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing expected_source not rejected: %v", err)
	}

	empty := writeSuite(t, `suite_id: s1`)
	if _, err := LoadRecallSuite(empty); err == nil {
		t.Error("empty suite not rejected")
	}
}

func TestCosine(t *testing.T) {
	cases := []struct {
		a, b []float32
		want float64
	}{
		{[]float32{1, 0}, []float32{1, 0}, 1},
		{[]float32{1, 0}, []float32{0, 1}, 0},
		{[]float32{1, 0}, []float32{-1, 0}, -1},
		{[]float32{1, 0}, []float32{0, 0}, 0}, // zero norm
		{[]float32{1, 0}, []float32{1}, 0},    // dim mismatch
		{[]float32{3, 4}, []float32{3, 4}, 1}, // not pre-normalized
	}
	for i, c := range cases {
		if got := Cosine(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("case %d: Cosine = %v, want %v", i, got, c.want)
		}
	}
}

func recallSents() ([]Sentence, [][]float32) {
	sents := []Sentence{
		{ID: "s1", Source: "patterns/backend.md"},
		{ID: "s2", Source: "patterns/frontend.md"},
		{ID: "s3", Source: "decisions/log.md"},
	}
	vecs := [][]float32{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	return sents, vecs
}

func TestTopK_RanksByCosineAndCaps(t *testing.T) {
	sents, vecs := recallSents()
	hits := TopK([]float32{0.9, 0.1, 0}, vecs, sents, 2)
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(hits))
	}
	if hits[0].SentenceID != "s1" || hits[1].SentenceID != "s2" {
		t.Errorf("ranking wrong: %+v", hits)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("scores not descending: %+v", hits)
	}
	if hits[0].Source != "patterns/backend.md" {
		t.Errorf("source not carried: %+v", hits[0])
	}

	all := TopK([]float32{1, 0, 0}, vecs, sents, 10)
	if len(all) != 3 {
		t.Errorf("k beyond corpus size should cap at %d, got %d", 3, len(all))
	}
}

func TestScoreResultsAndRecallAt(t *testing.T) {
	results := []QueryResult{
		{
			QueryID: "q1", ExpectedSource: "a.md",
			Hits: []Hit{{Source: "a.md"}, {Source: "b.md"}},
		},
		{
			QueryID: "q2", ExpectedSource: "b.md",
			Hits: []Hit{{Source: "x.md"}, {Source: "y.md"}, {Source: "z.md"}, {Source: "w.md"}, {Source: "v.md"}, {Source: "b.md"}},
		},
		{
			QueryID: "q3", ExpectedSource: "c.md",
			Hits: []Hit{{Source: "x.md"}},
		},
	}
	ScoreResults(results)
	if results[0].HitAt != 1 {
		t.Errorf("q1 HitAt = %d, want 1", results[0].HitAt)
	}
	if results[1].HitAt != 6 {
		t.Errorf("q2 HitAt = %d, want 6", results[1].HitAt)
	}
	if results[2].HitAt != 0 {
		t.Errorf("q3 HitAt = %d, want 0 (miss)", results[2].HitAt)
	}

	// q1 hits within 5; q2 only at rank 6 (outside recall@5); q3 misses.
	if got := RecallAt(results, 5); math.Abs(got-1.0/3.0) > 1e-9 {
		t.Errorf("RecallAt(5) = %v, want 1/3", got)
	}
	if got := RecallAt(results, 6); math.Abs(got-2.0/3.0) > 1e-9 {
		t.Errorf("RecallAt(6) = %v, want 2/3", got)
	}
	if got := RecallAt(nil, 5); got != 0 {
		t.Errorf("RecallAt(empty) = %v, want 0", got)
	}
}

func TestBuildReport_GateLogic(t *testing.T) {
	mk := func(r5 float64) EmbedderRecall {
		return EmbedderRecall{Recall5: r5, Results: make([]QueryResult, 50)}
	}

	// +25% relative → PASS
	rep := BuildReport("s1", mk(0.40), mk(0.50))
	if !rep.GatePass || math.Abs(rep.RelativeImprovement-0.25) > 1e-9 {
		t.Errorf("0.40→0.50: pass=%v imp=%v", rep.GatePass, rep.RelativeImprovement)
	}
	// +9.99…% relative (below 10%) → FAIL
	rep = BuildReport("s1", mk(0.50), mk(0.5499))
	if rep.GatePass {
		t.Errorf("sub-threshold improvement passed: imp=%v", rep.RelativeImprovement)
	}
	// exactly 10% → PASS (gate is ≥)
	rep = BuildReport("s1", mk(0.50), mk(0.55))
	if !rep.GatePass {
		t.Errorf("exact 10%% should pass: imp=%v", rep.RelativeImprovement)
	}
	// regression → FAIL
	rep = BuildReport("s1", mk(0.50), mk(0.40))
	if rep.GatePass {
		t.Error("regression passed")
	}
	// zero baseline, positive candidate → PASS
	rep = BuildReport("s1", mk(0), mk(0.10))
	if !rep.GatePass {
		t.Error("zero-baseline improvement failed")
	}
	// zero on both sides → FAIL (biased suite per retry policy)
	rep = BuildReport("s1", mk(0), mk(0))
	if rep.GatePass {
		t.Error("all-zero suite passed")
	}
	if rep.Queries != 50 {
		t.Errorf("Queries = %d, want 50", rep.Queries)
	}
}

func TestParseRemoteRecall(t *testing.T) {
	queries := []Query{
		{ID: "q01", ExpectedSource: "a.md"},
		{ID: "q02", ExpectedSource: "b.md"},
	}
	payload := `{"queries":[
		{"query_id":"q01","hits":[{"sentence_id":"s1","source":"a.md","score":0.91}]},
		{"query_id":"q02","hits":[{"sentence_id":"s2","source":"x.md","score":0.88}]}
	]}`
	stdout := []byte("modal noise\n===OLIFANT_RECALL_JSON===\n" + payload + "\n===END_OLIFANT_RECALL_JSON===\ntrailer\n")

	results, err := ParseRemoteRecall(stdout, queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d", len(results))
	}
	if results[0].HitAt != 1 {
		t.Errorf("q01 HitAt = %d, want 1", results[0].HitAt)
	}
	if results[1].HitAt != 0 {
		t.Errorf("q02 HitAt = %d, want 0", results[1].HitAt)
	}
	if results[0].Hits[0].Score != 0.91 {
		t.Errorf("score not carried: %+v", results[0].Hits[0])
	}
}

func TestParseRemoteRecall_Errors(t *testing.T) {
	queries := []Query{{ID: "q01", ExpectedSource: "a.md"}}

	if _, err := ParseRemoteRecall([]byte("no markers here"), queries); err == nil {
		t.Error("missing markers not rejected")
	}

	bad := []byte("===OLIFANT_RECALL_JSON===\n{nope\n===END_OLIFANT_RECALL_JSON===")
	if _, err := ParseRemoteRecall(bad, queries); err == nil {
		t.Error("malformed JSON not rejected")
	}

	other := []byte(`===OLIFANT_RECALL_JSON==={"queries":[{"query_id":"qXX","hits":[]}]}===END_OLIFANT_RECALL_JSON===`)
	if _, err := ParseRemoteRecall(other, queries); err == nil || !strings.Contains(err.Error(), "missing query") {
		t.Errorf("missing query not rejected: %v", err)
	}
}

func TestWriteRecallInputs(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "sentences.jsonl")
	qp := filepath.Join(dir, "queries.jsonl")
	sents := []Sentence{{ID: "s1", Text: "tenant filter", Source: "patterns/backend.md"}}
	queries := []Query{{ID: "q01", Text: "where is the filter?", ExpectedSource: "patterns/backend.md"}}

	if err := WriteRecallInputs(sp, qp, sents, queries); err != nil {
		t.Fatal(err)
	}

	var srow map[string]string
	sraw, _ := os.ReadFile(sp)
	if err := json.Unmarshal(sraw, &srow); err != nil {
		t.Fatalf("sentences row not JSON: %v", err)
	}
	if srow["id"] != "s1" || srow["source"] != "patterns/backend.md" || srow["text"] != "tenant filter" {
		t.Errorf("sentences row = %v", srow)
	}

	var qrow map[string]string
	qraw, _ := os.ReadFile(qp)
	if err := json.Unmarshal(qraw, &qrow); err != nil {
		t.Fatalf("queries row not JSON: %v", err)
	}
	if qrow["id"] != "q01" || qrow["text"] != "where is the filter?" {
		t.Errorf("queries row = %v", qrow)
	}
	if _, hasExpected := qrow["expected_source"]; hasExpected {
		t.Error("queries.jsonl must not leak expected_source to the remote side")
	}
}
