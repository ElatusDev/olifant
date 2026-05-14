package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/ollama"
	"gopkg.in/yaml.v3"
)

// Config drives a single validate run.
type Config struct {
	Claim       string   // verbatim claim text (already resolved from --claim file)
	Diff        string   // verbatim diff text (already resolved from --diff ref or file)
	OllamaURL   string
	Synthesizer string
	Temperature float64
	MaxTokens   int
	Verbose     bool
}

// Result is the validator's output.
type Result struct {
	RawJSON        string
	YAMLOutput     string
	JSONValid      bool
	Elapsed        time.Duration
	SynthMs        int64
	SynthEvalCount int
	SynthTokensSec float64
}

// Run executes one validate round against the configured executor.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.Temperature < 0 {
		cfg.Temperature = 0
	}

	prompt := buildPrompt(cfg.Claim, cfg.Diff)
	oc := ollama.New(cfg.OllamaURL)
	start := time.Now()
	resp, err := oc.Generate(ctx, ollama.GenerateRequest{
		Model:  cfg.Synthesizer,
		System: systemPrompt,
		Prompt: prompt,
		Format: ValidateJSONSchema,
		Options: map[string]interface{}{
			"temperature": cfg.Temperature,
			"num_predict": cfg.MaxTokens,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("validate: synth: %w", err)
	}
	elapsed := time.Since(start)
	yamlOut, jsonValid := jsonToYAML(resp.Response)
	tokensPerSec := 0.0
	if resp.EvalDuration > 0 && resp.EvalCount > 0 {
		tokensPerSec = float64(resp.EvalCount) / (float64(resp.EvalDuration) / 1e9)
	}
	return &Result{
		RawJSON:        strings.TrimSpace(resp.Response),
		YAMLOutput:     yamlOut,
		JSONValid:      jsonValid,
		Elapsed:        elapsed,
		SynthMs:        elapsed.Milliseconds(),
		SynthEvalCount: resp.EvalCount,
		SynthTokensSec: tokensPerSec,
	}, nil
}

// ExtractVerdict returns (overall_verdict, proceed) parsed from RawJSON.
func (r *Result) ExtractVerdict() (verdict, proceed string) {
	var shape struct {
		Validate struct {
			OverallVerdict string `json:"overall_verdict"`
			Proceed        string `json:"proceed"`
		} `json:"validate"`
	}
	if err := json.Unmarshal([]byte(r.RawJSON), &shape); err != nil {
		return "", ""
	}
	return shape.Validate.OverallVerdict, shape.Validate.Proceed
}

// ResolveDiff reads diff text from one of three sources:
//   * a literal git revision range (e.g., HEAD~1..HEAD) — runs git diff
//   * a path to an existing patch file — reads its contents
//   * a single commit SHA — runs git show --pretty=format:"" <sha>
func ResolveDiff(ref string, repoCwd string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("--diff is required")
	}
	// Heuristic: if it looks like a path that exists, read it.
	if isExistingFile(ref) {
		body, err := readFile(ref)
		if err != nil {
			return "", err
		}
		return body, nil
	}
	// Otherwise, treat as a git revision and run `git diff <ref>`.
	cmd := exec.Command("git", "diff", ref)
	if repoCwd != "" {
		cmd.Dir = repoCwd
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s: %w", ref, err)
	}
	if len(out) == 0 {
		// Try git show in case it's a single commit
		cmd2 := exec.Command("git", "show", ref)
		if repoCwd != "" {
			cmd2.Dir = repoCwd
		}
		out, err = cmd2.Output()
		if err != nil {
			return "", fmt.Errorf("git show %s: %w", ref, err)
		}
	}
	return string(out), nil
}

func buildPrompt(claim, diff string) string {
	var sb strings.Builder
	sb.WriteString("You are validating a Claude Code claim against the actual git diff produced.\n\n")
	sb.WriteString("CLAIM (what Claude said it did):\n```\n")
	sb.WriteString(strings.TrimSpace(claim))
	sb.WriteString("\n```\n\n")
	sb.WriteString("DIFF (what actually changed):\n```diff\n")
	// Cap diff size in prompt so we don't blow context budget.
	if len(diff) > 30000 {
		sb.WriteString(diff[:30000])
		sb.WriteString("\n… [diff truncated at 30000 chars]\n")
	} else {
		sb.WriteString(diff)
	}
	sb.WriteString("\n```\n\n")
	sb.WriteString("TASK:\n")
	sb.WriteString("1. Parse the CLAIM into atomic statements (claims_parsed[]).\n")
	sb.WriteString("2. For each atomic claim, decide if the DIFF evidences it: evidenced (clear evidence), partial (some evidence), or unmatched (no evidence). Be RIGOROUS: 'added tests' requires actual new test cases in the diff; 'updated X' requires X to appear in changed files.\n")
	sb.WriteString("3. Identify standards explicitly satisfied or violated (cite IDs only if you can name them — do not invent).\n")
	sb.WriteString("4. Overall verdict: validated (all claims evidenced) | partial (some evidenced, some not) | failed (claims demonstrably wrong or no evidence at all).\n")
	sb.WriteString("5. proceed: merge (overall=validated) | hold (partial) | block (failed).\n\n")
	sb.WriteString("Output JSON matching the schema. No prose.\n")
	return sb.String()
}

const systemPrompt = `You are Olifant — the post-Claude validator for the ElatusDev/AkademiaPlus platform.

Your job: read a claim Claude Code made (what it said it did) and the actual git diff (what changed), and produce an evidence-based verdict.

BE RIGOROUS. False-positive validation is dangerous — claiming work is done when it isn't ships bugs to main. When a claim is not clearly evidenced in the diff, mark it 'partial' or 'unmatched' rather than 'evidenced'.

HARD RULES:
1. Output JSON only, matching the schema.
2. Verdict ↔ proceed coupling: validated→merge, partial→hold, failed→block.
3. Evidence MUST point at concrete diff content — file path + line range + what changed. Vague phrasing like "the code adheres to standards" is unmatched, not evidenced.
4. Do not invent standard IDs. If you cannot name a real ID, leave standards_satisfied / standards_violated empty.`

// jsonToYAML — same helper as challenge.
func jsonToYAML(raw string) (string, bool) {
	var data interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return strings.TrimSpace(raw), false
	}
	out, err := yaml.Marshal(data)
	if err != nil {
		return strings.TrimSpace(raw), false
	}
	return strings.TrimRight(string(out), "\n"), true
}
