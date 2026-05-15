package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/ollama"
	"gopkg.in/yaml.v3"
)

// Config drives a single validate run.
type Config struct {
	Claim       string  // verbatim claim text (already resolved from --claim file)
	Diff        string  // verbatim diff text (already resolved from --diff ref or file)
	OllamaURL   string
	ChromaURL   string
	Embedder    string
	Synthesizer string
	Tenant      string
	Database    string
	Scopes      []string // retrieval scope filter; empty = default 7-scope union
	TopN        int      // retrieval top-N; default 8
	Temperature float64
	MaxTokens   int
	Verbose     bool

	// Validator is the optional cite-resolver. When supplied, retrieved
	// chunks ground the assessment and per-claim retry is enabled.
	Validator *challenge.CiteValidator

	// MaxValidateRetries — additional synth attempts on weak assessments.
	// 0 = no retry. Default when Validator is set: 1.
	MaxValidateRetries int
}

// defaultMaxTokens is the synthesizer num_predict default. 1024 was too
// short for multi-claim outputs with cites and evidence; raising to 4096
// gives enough headroom for ~12 claim assessments without truncation.
// Tunable per call via Config.MaxTokens.
const defaultMaxTokens = 4096

// Result is the validator's output.
type Result struct {
	RawJSON                 string
	YAMLOutput              string
	JSONValid               bool
	RetrievedCount          int
	RetrievedSources        []string
	Elapsed                 time.Duration
	EmbedMs                 int64
	RetrieveMs              int64
	SynthMs                 int64
	SynthEvalCount          int
	SynthTokensSec          float64
	ValidateAttempts        int // 1 = clean on first try; 2+ = retried after blockers
	RemainingViolations     []challenge.Violation
}

// Run executes one validate round against the configured executor.
// When Validator is wired, the pipeline is:
//   1. Retrieve top-N corpus chunks via Chroma using the claim as the
//      embedding query.
//   2. Synthesise an assessment with the retrieved context in the prompt.
//   3. Inspect the output via AssessmentValidator; on BLOCKER violations
//      retry once with a correction-guidance suffix.
//
// Without Validator, the pipeline degrades to a single synth call (no
// retrieval, no retry) — useful for unit tests and offline smokes.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.Temperature < 0 {
		cfg.Temperature = 0
	}
	if cfg.TopN <= 0 {
		cfg.TopN = 8
	}

	start := time.Now()

	// 1. Retrieval (best-effort grounding)
	var hits []RetrievedHit
	var embedMs, retrieveMs int64
	if cfg.Validator != nil && cfg.ChromaURL != "" && cfg.Embedder != "" {
		retrStart := time.Now()
		r, rerr := Retrieve(ctx, RetrievalConfig{
			OllamaURL: cfg.OllamaURL,
			ChromaURL: cfg.ChromaURL,
			Embedder:  cfg.Embedder,
			Tenant:    cfg.Tenant,
			Database:  cfg.Database,
			Scopes:    cfg.Scopes,
			TopN:      cfg.TopN,
			Verbose:   cfg.Verbose,
		}, cfg.Claim)
		retrieveMs = time.Since(retrStart).Milliseconds()
		if rerr != nil {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "validate: retrieval failed (%v) — proceeding without grounding\n", rerr)
			}
		} else {
			hits = r
			// Embed time is bundled in retrieveMs; report 0 to keep the
			// downstream telemetry simple. A future change can split them.
			embedMs = 0
		}
	}

	if cfg.Verbose && len(hits) > 0 {
		fmt.Println("  retrieved hits:")
		for i, h := range hits {
			fmt.Printf("    %2d  d=%.4f  [%s]  %v\n", i+1, h.Distance, h.Scope, h.Meta["source"])
		}
	}

	// 2. Build prompt + schema
	prompt := buildPrompt(cfg.Claim, cfg.Diff, hits)
	dynamicSchema := BuildValidateSchema(cfg.Validator, cfg.Scopes)

	oc := ollama.New(cfg.OllamaURL)
	gen := func(promptText string) (*ollama.GenerateResponse, error) {
		return oc.Generate(ctx, ollama.GenerateRequest{
			Model:  cfg.Synthesizer,
			System: systemPrompt,
			Prompt: promptText,
			Format: dynamicSchema,
			Options: map[string]interface{}{
				"temperature": cfg.Temperature,
				"num_predict": cfg.MaxTokens,
			},
		})
	}

	// 3. Synthesise (with retry on weak assessments)
	maxRetries := cfg.MaxValidateRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if cfg.Validator != nil && maxRetries == 0 {
		maxRetries = 1
	}

	av := &AssessmentValidator{Cite: cfg.Validator}

	synthStart := time.Now()
	resp, err := gen(prompt)
	if err != nil {
		return nil, fmt.Errorf("validate: synth: %w", err)
	}
	attempts := 1
	totalEvalCount := resp.EvalCount
	totalEvalDuration := resp.EvalDuration
	var lastViolations []challenge.Violation

	if cfg.Validator != nil {
		violations, vErr := av.Validate(resp.Response)
		if vErr != nil && cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  assessment-validator parse error: %v\n", vErr)
		}
		lastViolations = violations
		for challenge.HasBlockers(violations) && attempts <= maxRetries {
			blockers := challenge.FilterBlockers(violations)
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  validate retry #%d: %d BLOCKER + %d non-blocker\n",
					attempts, len(blockers), len(violations)-len(blockers))
				for _, vb := range blockers {
					fmt.Fprintf(os.Stderr, "    [%s] %s @ %s", vb.Code, vb.Note, vb.Location)
					if vb.Value != "" {
						fmt.Fprintf(os.Stderr, "  (%q)", vb.Value)
					}
					fmt.Fprintln(os.Stderr)
				}
			}
			retryPrompt := prompt + av.RetryPromptAddendum(violations, cfg.Scopes)
			retryResp, rerr := gen(retryPrompt)
			if rerr != nil {
				if cfg.Verbose {
					fmt.Fprintf(os.Stderr, "  retry generate failed: %v — keeping previous output\n", rerr)
				}
				break
			}
			attempts++
			totalEvalCount += retryResp.EvalCount
			totalEvalDuration += retryResp.EvalDuration
			resp = retryResp
			violations, _ = av.Validate(resp.Response)
			lastViolations = violations
			if !challenge.HasBlockers(violations) {
				break
			}
		}
	}
	synthMs := time.Since(synthStart).Milliseconds()

	yamlOut, jsonValid := jsonToYAML(resp.Response)

	tokensPerSec := 0.0
	if totalEvalDuration > 0 && totalEvalCount > 0 {
		tokensPerSec = float64(totalEvalCount) / (float64(totalEvalDuration) / 1e9)
	}

	sourcePaths := make([]string, 0, len(hits))
	for _, h := range hits {
		if src, ok := h.Meta["source"].(string); ok && src != "" {
			sourcePaths = append(sourcePaths, src)
		}
	}

	return &Result{
		RawJSON:             strings.TrimSpace(resp.Response),
		YAMLOutput:          yamlOut,
		JSONValid:           jsonValid,
		RetrievedCount:      len(hits),
		RetrievedSources:    sourcePaths,
		Elapsed:             time.Since(start),
		EmbedMs:             embedMs,
		RetrieveMs:          retrieveMs,
		SynthMs:             synthMs,
		SynthEvalCount:      totalEvalCount,
		SynthTokensSec:      tokensPerSec,
		ValidateAttempts:    attempts,
		RemainingViolations: lastViolations,
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
//   * a single commit SHA — runs git show <sha>
func ResolveDiff(ref string, repoCwd string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("--diff is required")
	}
	if isExistingFile(ref) {
		body, err := readFile(ref)
		if err != nil {
			return "", err
		}
		return body, nil
	}
	cmd := exec.Command("git", "diff", ref)
	if repoCwd != "" {
		cmd.Dir = repoCwd
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s: %w", ref, err)
	}
	if len(out) == 0 {
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

func buildPrompt(claim, diff string, hits []RetrievedHit) string {
	var sb strings.Builder
	sb.WriteString("You are validating a Claude Code claim against the actual git diff produced.\n\n")
	sb.WriteString("CLAIM (what Claude said it did):\n```\n")
	sb.WriteString(strings.TrimSpace(claim))
	sb.WriteString("\n```\n\n")

	if block := renderRetrievedBlock(hits); block != "" {
		sb.WriteString(block)
		sb.WriteString("\n")
	}

	sb.WriteString("DIFF (what actually changed):\n```diff\n")
	if len(diff) > 30000 {
		sb.WriteString(diff[:30000])
		sb.WriteString("\n… [diff truncated at 30000 chars]\n")
	} else {
		sb.WriteString(diff)
	}
	sb.WriteString("\n```\n\n")
	sb.WriteString("TASK:\n")
	sb.WriteString("1. Parse the CLAIM into atomic statements (claims_parsed[]). Each must have a unique id (\"c1\", \"c2\", …) and the verbatim claim text.\n")
	sb.WriteString("2. For EACH atomic claim in claims_parsed, emit a claim_assessments[] entry referencing the claim_id and:\n")
	sb.WriteString("   - verdict: evidenced (clear, specific evidence in DIFF) | partial (some but incomplete) | unmatched (no evidence).\n")
	sb.WriteString("   - evidence: concrete description (>=20 chars) — e.g., \"added test method shouldRejectExpiredToken() in core-api/auth/AuthFilterTest.java#L142-L168 covering the expiry path\".\n")
	sb.WriteString("   - cites[]: at least one entry when verdict is evidenced or partial. Use diff file paths (core-api/.../Foo.java#L42-L60) or standard IDs (D17, AP3, SB-04) that appear in the RETRIEVED CONTEXT. Empty [] is correct for unmatched.\n")
	sb.WriteString("3. standards_satisfied / standards_violated: cite artifact IDs ONLY when the diff demonstrably honors or breaks one. Empty [] is correct when none apply.\n")
	sb.WriteString("4. overall_verdict: validated (all claims evidenced) | partial (some unmatched/partial) | failed (claims demonstrably wrong or fabricated).\n")
	sb.WriteString("5. proceed: merge (validated) | hold (partial) | block (failed).\n\n")
	sb.WriteString("Output JSON matching the schema. No prose, no code fences.\n")
	return sb.String()
}

const systemPrompt = `You are Olifant — the post-Claude validator for the ElatusDev/AkademiaPlus platform.

Your job: read a claim Claude Code made (what it said it did) and the actual git diff (what changed), and produce an evidence-based per-claim assessment.

BE RIGOROUS. False-positive validation is dangerous — claiming work is done when it isn't ships bugs to main. When a claim is not clearly evidenced in the diff, mark it 'partial' or 'unmatched' rather than 'evidenced'.

ATOMIC-CLAIM DISCIPLINE:
- Break Claude's summary into single-fact atomic claims. "Added tests and updated docs" is TWO claims, not one.
- Each atomic claim gets its own claim_assessments[] entry with concrete evidence.
- Evidence must point at specific diff content: file path, line range, identifier name, what changed. Vague phrasing ("the code adheres to standards") is not evidence — that's unmatched.

CITE DISCIPLINE:
- cites[] for evidenced/partial verdicts: include at least one file path from the DIFF (e.g., core-api/foo/Bar.java#L42-L60) or a standard ID from the RETRIEVED CONTEXT (D17, AP3, SB-04). Empty cites[] are CORRECT for unmatched.
- standards_satisfied / standards_violated: only artifact IDs that appear in the RETRIEVED CONTEXT. Do NOT invent generic categories like "naming_conventions" or "single_responsibility_principle".

HARD RULES:
1. Output JSON only, matching the schema.
2. Verdict ↔ proceed coupling: validated→merge, partial→hold, failed→block.
3. If even one atomic claim is unmatched, overall_verdict cannot be 'validated' — use 'partial'.
4. claims_parsed and claim_assessments must be aligned 1:1 by claim_id.
5. Never fabricate standard IDs or file paths. If you can't cite a real one, leave the slot empty.`

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
