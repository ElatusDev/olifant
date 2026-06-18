// Claude backend for the synth seam — synthesis through the `claude` CLI
// subprocess (subscription-authenticated, no API key). The CLI invocation
// and result parsing live in internal/claudecli, shared with the PSP
// executor; this type is the thin synth-side adapter (workflow
// olifant-cloud-synth-v1, D-F4-6; consolidated in arch-consolidation-v1, F1).
//
// Known seam mismatches, accepted by the workflow:
//   - Request.Temperature is ignored — the CLI exposes no temperature
//     control (gate GF4 compensates with a double-run flip check).
//   - Request.MaxTokens is ignored — no CLI flag; output length is
//     bounded by the schema and the model's own stop behavior.
package synth

import (
	"context"
	"time"

	"github.com/ElatusDev/olifant/internal/claudecli"
)

// Claude runs synthesis through the claude CLI subprocess.
type Claude struct {
	binary  string
	effort  string
	timeout time.Duration
}

// NewClaude returns the claude-CLI synthesizer backend. The binary path
// must already be validated (config.ResolveClaude guarantees this when
// ok=true). The model is taken per-request from Request.Model.
func NewClaude(binary, effort string, timeout time.Duration) *Claude {
	return &Claude{binary: binary, effort: effort, timeout: timeout}
}

// Generate invokes the CLI once and returns the raw (schema-conformant) JSON
// text. EvalCount maps to output tokens; EvalDuration to wall-clock.
func (c *Claude) Generate(ctx context.Context, req Request) (*Response, error) {
	// Request.Schema is interface{} for backend-agnosticism; the CLI path
	// only ever receives a JSON-Schema object (map). A nil/non-map value
	// means "no schema" — claudecli gates on len.
	schema, _ := req.Schema.(map[string]interface{})
	res, err := claudecli.Run(ctx, c.binary, claudecli.Args{
		Prompt: req.Prompt,
		Model:  req.Model,
		Effort: c.effort,
		System: req.System,
		Schema: schema,
	}, claudecli.Options{Timeout: c.timeout})
	if err != nil {
		return nil, err
	}
	return &Response{
		Text:         res.Raw,
		EvalCount:    res.Usage.OutputTokens,
		EvalDuration: res.ElapsedNs,
	}, nil
}
