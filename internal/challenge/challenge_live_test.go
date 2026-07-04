//go:build integration

package challenge_test

import (
	"context"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/livetest"
)

// TestLive_ChallengeRun runs the full embed → retrieve → synth → validate flow
// against the live stack with the Ollama (qwen) synth backend. Synth=nil makes
// challenge.Run default to local Ollama, keeping the test fast (no claude).
func TestLive_ChallengeRun(t *testing.T) {
	rt := livetest.RequireStack(t)
	platformRoot, kbRoot := livetest.RequireKB(t)

	validator, err := challenge.NewCiteValidator(platformRoot, kbRoot)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	res, err := challenge.Run(ctx, challenge.Config{
		Request:            "Add a TenantScoped invoice entity to core-api with a composite key",
		OllamaURL:          rt.OllamaURL,
		ChromaURL:          rt.ChromaURL,
		Embedder:           rt.Embedder,
		Synthesizer:        rt.Synthesizer,
		Tenant:             rt.ChromaTenant,
		Database:           rt.ChromaDatabase,
		Scopes:             []string{"backend"},
		TopN:               6,
		MaxValidateRetries: 1,
		Validator:          validator,
	})
	if err != nil {
		t.Fatalf("challenge.Run: %v", err)
	}
	if !res.JSONValid {
		t.Errorf("synth output not valid JSON:\n%s", res.RawJSON)
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieval hits from the live corpus")
	}
	verdict, proceed := res.ExtractVerdict()
	if verdict == "" {
		t.Errorf("empty verdict in output:\n%s", res.RawJSON)
	}
	t.Logf("verdict=%s proceed=%s retrieved=%d attempts=%d blockers=%d",
		verdict, proceed, res.RetrievedCount, res.CiteAttempts, len(res.RemainingCiteViolations))
}
