//go:build integration

package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/eval"
	"github.com/ElatusDev/olifant/internal/livetest"
)

// TestLive_EvalRun runs a one-case eval suite end-to-end against the live stack
// (the same pipeline `olifant eval gate` exercises), with the Ollama synth
// backend for speed.
func TestLive_EvalRun(t *testing.T) {
	rt := livetest.RequireStack(t)
	platformRoot, kbRoot := livetest.RequireKB(t)
	t.Setenv("OLIFANT_SYNTH_BACKEND", "ollama")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	suite := &eval.Suite{
		SuiteID: "itest-smoke",
		Cases: []eval.Case{
			{
				ID:      "backend-tenant-scoped",
				Scope:   []string{"backend"},
				Request: "Add a TenantScoped invoice entity to core-api with a composite key",
			},
		},
	}

	outDir := t.TempDir()
	report, err := eval.Run(ctx, eval.RunConfig{
		Suite:        suite,
		PlatformRoot: platformRoot,
		KBRoot:       kbRoot,
		OutDir:       outDir,
	})
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	if report.TotalCases != 1 || len(report.Cases) != 1 {
		t.Fatalf("report cases = %d/%d, want 1/1", report.TotalCases, len(report.Cases))
	}
	cr := report.Cases[0]
	if cr.Error != "" {
		t.Fatalf("case errored: %s", cr.Error)
	}
	if cr.Verdict == "" {
		t.Errorf("empty verdict for case %s", cr.CaseID)
	}
	// report.yaml + per-case output written under the run dir.
	if _, err := os.Stat(filepath.Join(outDir, report.RunID, "report.yaml")); err != nil {
		t.Errorf("report.yaml not written: %v", err)
	}
	_ = rt
	t.Logf("run=%s verdict=%s blockers=%d retrieved=%d firstTry=%.2f",
		report.RunID, cr.Verdict, cr.Blockers, cr.RetrievedCount, report.FirstTryPassRate)
}
