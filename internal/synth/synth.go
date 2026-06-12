// Package synth defines the synthesizer seam: the minimal generate-with-
// schema surface shared by the prompt, challenge, and validate pipelines.
// Implementations: Ollama (local, the default) and — from Phase F4.2 — a
// claude-CLI subprocess backend. The seam exists so the synthesizer model
// can be swapped per backend without touching the call sites (workflow
// olifant-cloud-synth-v1-workflow.md, D-F4-2).
package synth

import "context"

// Request is the backend-agnostic synthesis request. Schema, when non-nil,
// is a JSON Schema (map[string]interface{}) the output must conform to.
type Request struct {
	Model       string
	System      string
	Prompt      string
	Schema      interface{}
	Temperature float64
	MaxTokens   int
}

// Response carries the raw model output plus generation telemetry.
// EvalCount/EvalDuration are zero when the backend does not report them.
type Response struct {
	Text         string
	EvalCount    int
	EvalDuration int64 // nanoseconds
}

// Client is implemented by every synthesizer backend.
type Client interface {
	Generate(ctx context.Context, req Request) (*Response, error)
}
