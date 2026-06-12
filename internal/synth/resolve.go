package synth

import (
	"fmt"

	"github.com/ElatusDev/olifant/internal/config"
)

// FromRuntime selects the synthesizer backend from OLIFANT_SYNTH_BACKEND
// and returns the client plus the effective default model for that backend
// (rt.Synthesizer for ollama; rt.SynthClaudeModel for claude — the model
// gate GF4 passed on, pinned per workflow §0.7's promote constraint).
// Callers may still override the model per invocation (--synth flag, suite
// synth: overrides) — the model travels in Request.Model.
func FromRuntime(rt config.Runtime) (Client, string, error) {
	switch rt.SynthBackend {
	case "", "ollama":
		return NewOllama(rt.OllamaURL), rt.Synthesizer, nil
	case "claude":
		cc, ok := config.ResolveClaude()
		if !ok {
			return nil, "", fmt.Errorf("OLIFANT_SYNTH_BACKEND=claude but the claude binary is not on PATH (set OLIFANT_CLAUDE_BINARY or install the CLI)")
		}
		// Timeout 0: synthesis defers to the caller's context deadline
		// (eval case budget, CLI --timeout). OLIFANT_CLAUDE_TIMEOUT's
		// 120 s default is sized for short PSP steps; layering it under
		// the case budget killed long-tail synth calls outright instead
		// of letting them finish (F4.4 iteration finding — case 4,
		// `signal: killed` at attempts=0 with 120 s of budget left).
		model := rt.SynthClaudeModel
		if model == "" {
			model = cc.Model
		}
		return NewClaude(cc.Binary, cc.Effort, 0), model, nil
	default:
		return nil, "", fmt.Errorf("unknown OLIFANT_SYNTH_BACKEND %q (want ollama or claude)", rt.SynthBackend)
	}
}
