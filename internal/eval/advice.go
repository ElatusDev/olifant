package eval

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/advice"
	"github.com/ElatusDev/olifant/internal/config"
)

// runAdviceCase runs the T1 retrieve --file pipeline (internal/advice) on a
// case's inline code and scores whether each expected cite surfaced in its
// bucket. Retrieval-only, measurement — never a BLOCKER (D269). olifant#110.
func runAdviceCase(ctx context.Context, c Case, caseDir string, caseStart time.Time, rt config.Runtime, topN, timeoutSec int) CaseResult {
	caseCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	res := CaseResult{CaseID: c.ID, Scope: c.Scope, Attempts: 1}
	ares, err := advice.Run(caseCtx, advice.Config{
		CodeBody:  c.FileContent,
		Scopes:    c.Scope,
		OllamaURL: rt.OllamaURL,
		ChromaURL: rt.ChromaURL,
		Embedder:  rt.Embedder,
		Tenant:    rt.ChromaTenant,
		Database:  rt.ChromaDatabase,
		TopN:      topN,
	})
	res.ElapsedMs = time.Since(caseStart).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.RetrievedCount = len(ares.Chunks)
	res.EmbedMs = ares.EmbedMs
	res.RetrieveMs = ares.RetrieveMs
	res.AdviceScore = scoreAdvice(c.Advice, ares)

	// Persist the surfaced cites per bucket for forensics.
	outYAML := filepath.Join(caseDir, "advice.yaml")
	if b, merr := yaml.Marshal(map[string]interface{}{
		"avoid":      ares.Cites("avoid"),
		"prefer":     ares.Cites("prefer"),
		"convention": ares.Cites("convention"),
		"sources":    ares.Sources,
	}); merr == nil {
		if werr := os.WriteFile(outYAML, b, 0o644); werr == nil {
			res.OutputYAMLPath = outYAML
		}
	}
	return res
}

// scoreAdvice checks each expected cite surfaced in its named bucket.
func scoreAdvice(exp *AdviceExpected, r *advice.Result) *AdviceScore {
	s := &AdviceScore{
		Avoid:      scoreBucket(exp.ExpectAvoid, r.Cites("avoid")),
		Prefer:     scoreBucket(exp.ExpectPrefer, r.Cites("prefer")),
		Convention: scoreBucket(exp.ExpectConvention, r.Cites("convention")),
	}
	s.Passed = len(s.Avoid.Missed) == 0 && len(s.Prefer.Missed) == 0 && len(s.Convention.Missed) == 0
	return s
}

func scoreBucket(expected, got []string) BucketScore {
	gotSet := make(map[string]bool, len(got))
	for _, g := range got {
		gotSet[g] = true
	}
	bs := BucketScore{Expected: expected}
	for _, e := range expected {
		if gotSet[e] {
			bs.Hit = append(bs.Hit, e)
		} else {
			bs.Missed = append(bs.Missed, e)
		}
	}
	return bs
}
