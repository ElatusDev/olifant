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

// ToOllamaRequest maps a Request onto the Ollama wire shape. Exported so
// the prompt package's OLIFANT_PROMPT_DEBUG dump stays byte-identical to
// what actually goes over the wire.
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
