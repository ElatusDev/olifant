package psp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ElatusDev/olifant/internal/ollama"
)

// Executor is the v0 abstraction over "the thing that runs a step".
// v0: LocalExecutor (Ollama). v1+: ClaudeAPIExecutor, ClaudeCodeSubprocessExecutor.
type Executor interface {
	ID() string                       // for SYN_ACK
	Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error)
}

// Response from an executor — the raw text + parsed structured output.
//
// CacheCreationTokens / CacheReadTokens are populated by executors that
// support prompt caching (Claude). They are zero for executors without
// caching semantics (Ollama). Surfacing them lets the aggregate report
// cache hit rate per plan, which is the central cost metric for hybrid
// execution.
type Response struct {
	RawText             string
	Output              StepOutput
	EvalTokens          int
	PromptTokens        int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	TotalDurationNs     int64
}

// LocalExecutor backs the prompt-runner with Ollama-hosted models. v0 ships
// this as the default. Per psp-v1.md §10.
type LocalExecutor struct {
	client *ollama.Client
	model  string
}

// NewLocalExecutor wires an Ollama client + model.
func NewLocalExecutor(baseURL, model string) *LocalExecutor {
	return &LocalExecutor{
		client: ollama.New(baseURL),
		model:  model,
	}
}

func (e *LocalExecutor) ID() string { return e.model }

// Execute sends one prompt with the given JSON Schema constraint and returns
// the parsed Response. Empty schema disables grammar-constrained decoding.
func (e *LocalExecutor) Execute(ctx context.Context, prompt string, schema map[string]interface{}) (Response, error) {
	req := ollama.GenerateRequest{
		Model:  e.model,
		Prompt: prompt,
		Options: map[string]interface{}{
			"temperature": 0,
			"num_predict": 1024,
		},
	}
	if len(schema) > 0 {
		req.Format = schema
	}
	resp, err := e.client.Generate(ctx, req)
	if err != nil {
		return Response{}, fmt.Errorf("executor.Execute: %w", err)
	}
	out := Response{
		RawText:         strings.TrimSpace(resp.Response),
		EvalTokens:      resp.EvalCount,
		PromptTokens:    resp.PromptEvalCount,
		OutputTokens:    resp.EvalCount,
		TotalDurationNs: resp.TotalDuration,
	}
	if len(schema) > 0 {
		var parsed StepOutput
		if uerr := json.Unmarshal([]byte(out.RawText), &parsed); uerr != nil {
			return out, fmt.Errorf("executor: response is not valid JSON: %w", uerr)
		}
		out.Output = parsed
	}
	return out, nil
}
