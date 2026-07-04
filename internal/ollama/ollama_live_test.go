//go:build integration

package ollama_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/ollama"
)

func TestLive_Version(t *testing.T) {
	rt := livetest.RequireOllama(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	v, err := ollama.New(rt.OllamaURL).Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if strings.TrimSpace(v) == "" {
		t.Error("empty version string from live Ollama")
	}
	t.Logf("ollama version: %s", v)
}

func TestLive_Embed(t *testing.T) {
	rt := livetest.RequireOllama(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	inputs := []string{"tenant scoped entity", "composite key pattern"}
	embs, err := ollama.New(rt.OllamaURL).Embed(ctx, rt.Embedder, inputs)
	if err != nil {
		t.Fatalf("Embed(%s): %v", rt.Embedder, err)
	}
	if len(embs) != len(inputs) {
		t.Fatalf("got %d embeddings for %d inputs", len(embs), len(inputs))
	}
	dim := len(embs[0])
	if dim == 0 {
		t.Fatal("embedding vector is empty")
	}
	// All vectors share one dimensionality.
	for i, e := range embs {
		if len(e) != dim {
			t.Errorf("embedding %d has dim %d, want %d", i, len(e), dim)
		}
	}
	t.Logf("embedder=%s dim=%d", rt.Embedder, dim)
}

func TestLive_Embed_Empty(t *testing.T) {
	rt := livetest.RequireOllama(t)
	out, err := ollama.New(rt.OllamaURL).Embed(context.Background(), rt.Embedder, nil)
	if err != nil || out != nil {
		t.Errorf("empty Embed = (%v, %v), want (nil, nil)", out, err)
	}
}

func TestLive_Generate(t *testing.T) {
	rt := livetest.RequireOllama(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	resp, err := ollama.New(rt.OllamaURL).Generate(ctx, ollama.GenerateRequest{
		Model:  rt.Synthesizer,
		System: "You output only JSON.",
		Prompt: `Output exactly this JSON and nothing else: {"reply":"ok"}`,
		Format: "json",
		Options: map[string]interface{}{
			"temperature": 0,
			"num_predict": 64,
		},
	})
	if err != nil {
		t.Fatalf("Generate(%s): %v", rt.Synthesizer, err)
	}
	if strings.TrimSpace(resp.Response) == "" {
		t.Error("empty Generate response from live Ollama")
	}
	if !resp.Done {
		t.Error("Generate response not marked done")
	}
	t.Logf("synth=%s eval_count=%d tok/s=%.1f resp=%q",
		rt.Synthesizer, resp.EvalCount, resp.TokensPerSec(), strings.TrimSpace(resp.Response))
}
