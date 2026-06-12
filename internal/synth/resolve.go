package synth

import (
	"fmt"

	"github.com/ElatusDev/olifant/internal/config"
)

// FromRuntime selects the synthesizer backend from OLIFANT_SYNTH_BACKEND
// and returns the client plus the effective default model for that backend
// (rt.Synthesizer for ollama; OLIFANT_CLAUDE_MODEL for claude). Callers may
// still override the model per invocation (--synth flag, suite synth:
// overrides) — the model travels in Request.Model.
func FromRuntime(rt config.Runtime) (Client, string, error) {
	switch rt.SynthBackend {
	case "", "ollama":
		return NewOllama(rt.OllamaURL), rt.Synthesizer, nil
	case "claude":
		cc, ok := config.ResolveClaude()
		if !ok {
			return nil, "", fmt.Errorf("OLIFANT_SYNTH_BACKEND=claude but the claude binary is not on PATH (set OLIFANT_CLAUDE_BINARY or install the CLI)")
		}
		return NewClaude(cc.Binary, cc.Effort, cc.Timeout), cc.Model, nil
	default:
		return nil, "", fmt.Errorf("unknown OLIFANT_SYNTH_BACKEND %q (want ollama or claude)", rt.SynthBackend)
	}
}
