//go:build integration

package prompt_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/prompt"
)

// TestLive_PromptBuild runs embed → retrieve → synth → validate → write against
// the live stack (Ollama synth), producing a real PSP plan file.
func TestLive_PromptBuild(t *testing.T) {
	rt := livetest.RequireStack(t)
	// Plan synthesis emits a large structured PSP document — markedly slower on
	// qwen than a single challenge verdict, so allow generous headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	outDir := t.TempDir()
	res, err := prompt.Build(ctx, prompt.Config{
		Goal:        "Add a TenantScoped invoice entity to core-api with a composite key and soft delete",
		OllamaURL:   rt.OllamaURL,
		ChromaURL:   rt.ChromaURL,
		Embedder:    rt.Embedder,
		Synthesizer: rt.Synthesizer,
		Tenant:      rt.ChromaTenant,
		Database:    rt.ChromaDatabase,
		Scopes:      []string{"backend"},
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("prompt.Build: %v", err)
	}
	if res.StepCount == 0 {
		t.Error("plan has 0 steps")
	}
	if res.RetrievedCount == 0 {
		t.Error("expected retrieval hits from the live corpus")
	}
	if _, err := os.Stat(res.PlanPath); err != nil {
		t.Errorf("plan file not written: %v", err)
	}
	t.Logf("plan=%s steps=%d retrieved=%d split=%v synthMs=%d",
		res.PlanID, res.StepCount, res.RetrievedCount, res.Split, res.SynthMs)
}
