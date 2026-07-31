package synth

import (
	"context"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// Ollama adapts the local Ollama HTTP API to the Client seam.
type Ollama struct {
	oc *ollama.Client
}

// NewOllama returns the default (local) synthesizer backend.
func NewOllama(baseURL string) *Ollama {
	return &Ollama{oc: ollama.New(baseURL)}
}

// WithKeepAlive sets the client-level keep_alive injected on generate
// calls (epic #119 S4). Injection happens in ollama.Client.Generate —
// NOT in ToOllamaRequest — so the OLIFANT_PROMPT_DEBUG dump stays
// byte-identical to the mapper output (workflow D-1/IA6).
func (o *Ollama) WithKeepAlive(v string) *Ollama {
	o.oc.KeepAlive = v
	return o
}

// ToOllamaRequest maps a Request onto the Ollama wire shape — the prompt,
// system, options, and schema bytes. Exported for the prompt package's
// OLIFANT_PROMPT_DEBUG dump. Since #123 the wire body may additionally
// carry a client-injected `keep_alive` (transport tuning, outside the
// prompt/cache-prefix bytes) that the dump deliberately omits — a replayed
// dump reproduces the model interaction, not the residency hint.
func ToOllamaRequest(req Request) ollama.GenerateRequest {
	return ollama.GenerateRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
		System: req.System,
		Options: map[string]interface{}{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
		},
		Format: req.Schema,
	}
}

// Generate runs one non-streamed completion.
func (o *Ollama) Generate(ctx context.Context, req Request) (*Response, error) {
	resp, err := o.oc.Generate(ctx, ToOllamaRequest(req))
	if err != nil {
		return nil, err
	}
	return &Response{
		Text:         resp.Response,
		EvalCount:    resp.EvalCount,
		EvalDuration: resp.EvalDuration,
	}, nil
}
