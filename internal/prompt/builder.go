package prompt

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/psp"
	"gopkg.in/yaml.v3"
)

// stepIDPattern enforces the PSP step_NN id format post-parse. The synth
// schema no longer carries this constraint (it crashed Ollama's grammar
// engine on nested schemas) — see synth.go's planSynthSchema comment.
var stepIDPattern = regexp.MustCompile(`^step_\d+$`)

// Config drives one Build call. Endpoint + model fields mirror challenge.Config
// so callers can populate from config.Resolve() the same way.
type Config struct {
	Goal        string
	OllamaURL   string
	ChromaURL   string
	Embedder    string
	Synthesizer string
	Tenant      string
	Database    string
	Scopes      []string // empty = all
	TopN        int      // default 8
	Temperature float64  // default 0.1; 0 = greedy
	MaxTokens   int      // default 4096 — plans need more room than challenge
	OutDir      string   // default "plans"
	Verbose     bool
}

// Result is the outcome of one Build call.
type Result struct {
	PlanID           string
	PlanPath         string   // primary plan file (first sub-plan if split)
	SubPlanPaths     []string // populated when Split is true
	StepCount        int      // total steps across the logical plan (pre-split)
	Scope            []string
	RetrievedCount   int
	RetrievedSources []string
	RawSynthJSON     string
	Elapsed          time.Duration
	EmbedMs          int64
	RetrieveMs       int64
	SynthMs          int64
	SynthEvalCount   int
	SynthTokensSec   float64
	Split            bool
	// Warnings surfaces non-fatal issues found during build. Today it
	// records sub-plan validation failures from psp.Validate after split —
	// cross-sub-plan depends_on entries are spec-legal but rejected by the
	// strict validator. Caller decides what to do with them.
	Warnings []string
}

// Build runs the full pipeline: retrieve → synth → transform → validate →
// split-if-needed → write. Returns the emitted plan paths in a Result.
func Build(ctx context.Context, cfg Config) (*Result, error) {
	start := time.Now()
	cfg = applyDefaults(cfg)

	if strings.TrimSpace(cfg.Goal) == "" {
		return nil, fmt.Errorf("Build: Goal is required")
	}

	hits, embedMs, retrieveMs, err := retrieve(ctx, retrieveConfig{
		Goal:      cfg.Goal,
		OllamaURL: cfg.OllamaURL,
		ChromaURL: cfg.ChromaURL,
		Embedder:  cfg.Embedder,
		Tenant:    cfg.Tenant,
		Database:  cfg.Database,
		Scopes:    cfg.Scopes,
		TopN:      cfg.TopN,
		Verbose:   cfg.Verbose,
	})
	if err != nil {
		return nil, err
	}

	sr, err := synthesize(ctx, synthConfig{
		OllamaURL:   cfg.OllamaURL,
		Synthesizer: cfg.Synthesizer,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	}, cfg.Goal, hits)
	if err != nil {
		return nil, err
	}

	res, err := buildFromSynthJSON(sr.RawJSON, cfg.Goal, cfg.OutDir, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	tokensPerSec := 0.0
	if sr.EvalDuration > 0 && sr.EvalCount > 0 {
		tokensPerSec = float64(sr.EvalCount) / (float64(sr.EvalDuration) / 1e9)
	}
	res.RetrievedCount = len(hits)
	res.RetrievedSources = sourcePathsFromHits(hits)
	res.RawSynthJSON = sr.RawJSON
	res.Elapsed = time.Since(start)
	res.EmbedMs = embedMs
	res.RetrieveMs = retrieveMs
	res.SynthMs = sr.ElapsedMs
	res.SynthEvalCount = sr.EvalCount
	res.SynthTokensSec = tokensPerSec
	return res, nil
}

// applyDefaults fills zero-valued fields with sensible defaults.
func applyDefaults(cfg Config) Config {
	if cfg.TopN <= 0 {
		cfg.TopN = 8
	}
	if cfg.MaxTokens <= 0 {
		// Match challenge's 1024 default. Larger goals may need raising
		// via --max-tokens, but the current rig (4096 num_ctx loaded on
		// olifant mini) can't safely exceed ~1500 output tokens after
		// the prompt is accounted for.
		cfg.MaxTokens = 1024
	}
	if cfg.Temperature < 0 {
		cfg.Temperature = 0
	}
	if strings.TrimSpace(cfg.OutDir) == "" {
		cfg.OutDir = "plans"
	}
	return cfg
}

// buildFromSynthJSON is the post-synth pipeline: parse → transform → split
// (if needed) → validate → write. Split out from Build so tests can drive it
// with canned synth output without standing up Ollama or Chroma.
func buildFromSynthJSON(rawJSON, fallbackGoal, outDir string, ts time.Time) (*Result, error) {
	plan, err := transformSynthJSONToPlan(rawJSON, fallbackGoal)
	if err != nil {
		preview := rawJSON
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("transform synth output: %w (raw=%q)", err, preview)
	}

	plan.PlanID = generatePlanID(ts, fallbackGoal)
	plan.CreatedAt = ts.UTC().Format(time.RFC3339)
	plan.CreatedBy = "olifant-prompt-builder"

	// Validate the LOGICAL plan first — catches duplicate IDs, dep cycles,
	// unknown deps in the pre-split form. psp.Validate enforces the MPS cap
	// which we'd hit on over-cap plans, so we run our own subset here.
	if err := validateLogicalPlan(plan); err != nil {
		return nil, fmt.Errorf("synthesized plan failed logical validation: %w", err)
	}

	totalSteps := len(plan.Steps)

	if len(plan.Steps) > psp.MaxStepsPerPlan {
		parts := psp.Split(plan)
		if parts == nil {
			return nil, fmt.Errorf("plan has %d steps but Split returned nil", len(plan.Steps))
		}
		paths := make([]string, 0, len(parts))
		var warnings []string
		for _, p := range parts {
			// Sub-plan validation is best-effort. Cross-boundary depends_on
			// entries are spec-legal (resolved at runtime via seeded_from)
			// but rejected by psp.Validate's strict within-plan dep check.
			// We record those as Result.Warnings so the user can re-scope;
			// the file is still written. In practice, well-shaped goals
			// don't produce cross-boundary deps.
			if verr := psp.Validate(p); verr != nil {
				warnings = append(warnings,
					fmt.Sprintf("sub-plan %s validation: %v (likely cross-boundary depends_on; seed mechanism handles at runtime)",
						p.PlanID, verr))
			}
			ppath, werr := writePlan(outDir, p)
			if werr != nil {
				return nil, werr
			}
			paths = append(paths, ppath)
		}
		return &Result{
			PlanID:       plan.PlanID,
			PlanPath:     paths[0],
			SubPlanPaths: paths,
			StepCount:    totalSteps,
			Scope:        plan.Scope,
			Split:        true,
			Warnings:     warnings,
		}, nil
	}

	if verr := psp.Validate(plan); verr != nil {
		return nil, fmt.Errorf("synthesized plan failed validation: %w", verr)
	}
	ppath, werr := writePlan(outDir, plan)
	if werr != nil {
		return nil, werr
	}
	return &Result{
		PlanID:    plan.PlanID,
		PlanPath:  ppath,
		StepCount: totalSteps,
		Scope:     plan.Scope,
	}, nil
}

// synthShape is the on-wire JSON shape emitted by the synthesizer. Mirrors
// planSynthSchema.
type synthShape struct {
	Plan struct {
		Goal  string      `json:"goal"`
		Scope []string    `json:"scope,omitempty"`
		Steps []synthStep `json:"steps"`
	} `json:"plan"`
}

type synthStep struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Signals        []string           `json:"signals,omitempty"`
	DependsOn      []string           `json:"depends_on,omitempty"`
	ExpectedOutput synthExpectedShape `json:"expected_output"`
}

type synthExpectedShape struct {
	Type   string   `json:"type"`
	Fields []string `json:"fields,omitempty"`
}

// transformSynthJSONToPlan parses synth output and constructs a psp.Plan.
// PlanID / CreatedAt / CreatedBy are NOT set here — Build wires those after.
func transformSynthJSONToPlan(raw, fallbackGoal string) (*psp.Plan, error) {
	var s synthShape
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("synth output is not valid JSON: %w", err)
	}
	if len(s.Plan.Steps) == 0 {
		return nil, fmt.Errorf("synth produced 0 steps")
	}
	plan := &psp.Plan{
		Goal:  s.Plan.Goal,
		Scope: s.Plan.Scope,
	}
	if strings.TrimSpace(plan.Goal) == "" {
		plan.Goal = fallbackGoal
	}
	for _, ss := range s.Plan.Steps {
		if !stepIDPattern.MatchString(ss.ID) {
			return nil, fmt.Errorf("step id %q does not match PSP step_NN format", ss.ID)
		}
		for _, dep := range ss.DependsOn {
			if !stepIDPattern.MatchString(dep) {
				return nil, fmt.Errorf("step %q depends_on entry %q does not match step_NN format", ss.ID, dep)
			}
		}
		plan.Steps = append(plan.Steps, psp.Step{
			ID:          ss.ID,
			Name:        ss.Name,
			Description: ss.Description,
			Signals:     ss.Signals,
			DependsOn:   ss.DependsOn,
			ExpectedOutput: psp.ExpectedOutput{
				Schema: synthExpectedToSchema(ss.ExpectedOutput.Type, ss.ExpectedOutput.Fields),
			},
			RetryPolicy: psp.RetryPolicy{MaxAttempts: 2, BackoffMs: 500},
		})
	}
	return plan, nil
}

// synthExpectedToSchema turns the synth's compact expected_output shape into
// a JSON Schema fragment suitable for psp.ExpectedOutput.Schema.
func synthExpectedToSchema(typ string, fields []string) map[string]interface{} {
	if typ == "" {
		typ = "object"
	}
	schema := map[string]interface{}{"type": typ}
	if typ == "object" && len(fields) > 0 {
		props := map[string]interface{}{}
		for _, f := range fields {
			props[f] = map[string]interface{}{"type": "string"}
		}
		schema["properties"] = props
		required := append([]string(nil), fields...)
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// validateLogicalPlan runs psp.Validate's structural checks (unique IDs,
// resolvable depends_on, no cycles) WITHOUT enforcing MaxStepsPerPlan, so
// it can validate the pre-split logical plan. psp.Split is responsible for
// producing structurally-correct sub-plans afterwards.
func validateLogicalPlan(p *psp.Plan) error {
	if p == nil {
		return fmt.Errorf("plan is nil")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	seen := map[string]bool{}
	stepIDs := map[string]bool{}
	for _, s := range p.Steps {
		if s.ID == "" {
			return fmt.Errorf("step has empty id")
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate step id: %s", s.ID)
		}
		seen[s.ID] = true
		stepIDs[s.ID] = true
	}
	// Verify every depends_on references a known step. Cycle detection is
	// covered by psp.Validate / psp.Split's topoSort once split happens,
	// but in the single-plan path we also run psp.Validate which catches it.
	for _, s := range p.Steps {
		for _, dep := range s.DependsOn {
			if !stepIDs[dep] {
				return fmt.Errorf("step %q depends on unknown step %q", s.ID, dep)
			}
		}
	}
	return nil
}

// generatePlanID returns a chronologically-sortable, content-seeded plan_id
// matching the PSP v1 examples: 2026-05-14T20-15-00Z-abc123.
func generatePlanID(ts time.Time, seed string) string {
	stamp := ts.UTC().Format("2006-01-02T15-04-05Z")
	h := sha1.New()
	h.Write([]byte(stamp))
	h.Write([]byte{0})
	h.Write([]byte(seed))
	return fmt.Sprintf("%s-%s", stamp, hex.EncodeToString(h.Sum(nil))[:6])
}

// writePlan serialises a plan to <outDir>/<plan_id>.yaml. Returns the
// absolute written path.
func writePlan(outDir string, plan *psp.Plan) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	name := strings.ReplaceAll(plan.PlanID, "/", "_") + ".yaml"
	path := filepath.Join(outDir, name)
	header := fmt.Sprintf(
		"# Olifant PSP plan — generated by `olifant prompt build`.\n"+
			"# Schema: knowledge-base/dsl/psp-v1.md §3.4 + internal/psp/types.go (Plan).\n"+
			"# plan_id:  %s\n"+
			"# created:  %s\n\n",
		plan.PlanID, time.Now().UTC().Format(time.RFC3339))
	body, err := yaml.Marshal(plan)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(header+string(body)), 0o644); err != nil {
		return "", err
	}
	abs, aerr := filepath.Abs(path)
	if aerr != nil {
		return path, nil
	}
	return abs, nil
}
