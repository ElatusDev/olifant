package format

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// GenConfig drives the Phase C1 paraphrase + verdict-YAML pipeline.
type GenConfig struct {
	Archetypes      []ArchetypeDef // empty = all from Archetypes()
	VariantsPerArch int            // default 30
	OutPath         string         // default ~/.olifant/training/format-v1/pairs.jsonl
	ClaudeBin       string         // default "claude"
	Model           string         // default "opus" (per feedback_olifant_uses_opus_latest)
	Resume          bool           // skip prompts already in OutPath
	Concurrency     int            // parallel verdict-YAML calls; default 1
	MaxRetries      int            // per-call retry on parse/validation fail; default 1
	Verbose         bool
	PerCallTimeout  time.Duration // default 90s
}

// Pair is one (prompt, verdict-YAML) training row. Persists as one JSON
// object per line in the JSONL output file.
type Pair struct {
	Prompt      string `json:"prompt"`
	Response    string `json:"response"` // verdict-YAML literal
	Archetype   string `json:"archetype"`
	Verdict     string `json:"verdict"` // for forensics
	GeneratedAt string `json:"generated_at"`
}

// Stats summarises one Generate run.
type Stats struct {
	ArchetypesProcessed int
	VariantsAttempted   int
	VariantsAccepted    int
	VariantsRetried     int
	VariantsRejected    int
	StageOneCalls       int
	StageTwoCalls       int
	StageOneFailures    int
	StageTwoFailures    int
	StageOneElapsed     time.Duration
	StageTwoElapsed     time.Duration
	TotalElapsed        time.Duration
}

// Generate runs the two-stage Opus pipeline.
//
//	Stage 1 — for each archetype, one Opus call returns ~N paraphrastic
//	          variants of the seed request (same intent + verdict).
//	Stage 2 — for each variant, one Opus call returns a verdict-YAML
//	          string conforming to schema.go's tight rules.
//
// Both stages route LLM calls through `claude --print --model opus` per
// feedback_olifant_uses_claude_code_only.md + _opus_latest.md.
//
// The OutPath is append-only JSONL — Generate re-reads existing pairs
// on Resume==true and skips prompts already present.
func Generate(ctx context.Context, cfg GenConfig) (Stats, error) {
	cfg = applyDefaults(cfg)
	var stats Stats
	runStart := time.Now()

	if err := os.MkdirAll(filepath.Dir(cfg.OutPath), 0o755); err != nil {
		return stats, fmt.Errorf("mkdir: %w", err)
	}

	// Resume: snapshot which prompts already exist on disk.
	existing := map[string]bool{}
	if cfg.Resume {
		seen, err := readExistingPrompts(cfg.OutPath)
		if err != nil {
			return stats, fmt.Errorf("scan existing: %w", err)
		}
		existing = seen
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "resume: %d prompts already on disk\n", len(existing))
		}
	}

	// Open the JSONL file for append.
	f, err := os.OpenFile(cfg.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return stats, fmt.Errorf("open out: %w", err)
	}
	defer f.Close()
	// Per-line append must be flushed individually; concurrent writes
	// serialise via writeMu so JSONL lines don't interleave.
	var writeMu sync.Mutex

	archetypes := cfg.Archetypes
	if len(archetypes) == 0 {
		archetypes = Archetypes()
	}

	// =========================
	// Stage 1 — paraphrastic variants per archetype
	// =========================
	stage1Start := time.Now()
	type archVariants struct {
		arch     ArchetypeDef
		variants []string
	}
	allVariants := make([]archVariants, 0, len(archetypes))
	for _, a := range archetypes {
		stats.StageOneCalls++
		variants, err := paraphraseVariants(ctx, cfg, a)
		if err != nil {
			stats.StageOneFailures++
			fmt.Fprintf(os.Stderr, "stage1 [%s]: %v — skipping archetype\n", a.ID, err)
			continue
		}
		stats.ArchetypesProcessed++
		allVariants = append(allVariants, archVariants{arch: a, variants: variants})
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "stage1 [%s] -> %d variants\n", a.ID, len(variants))
		}
	}
	stats.StageOneElapsed = time.Since(stage1Start)

	// =========================
	// Stage 2 — verdict-YAML per variant, with concurrency
	// =========================
	stage2Start := time.Now()
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	var processed int64
	for _, av := range allVariants {
		for _, variant := range av.variants {
			if existing[variant] {
				continue
			}
			stats.VariantsAttempted++
			sem <- struct{}{}
			wg.Add(1)
			go func(arch ArchetypeDef, prompt string) {
				defer wg.Done()
				defer func() { <-sem }()
				pair, accepted, retried, callErr := synthVerdict(ctx, cfg, arch, prompt)
				if retried {
					atomic.AddInt64(&processed, 0) // touch — no-op, kept for symmetry
				}
				if callErr != nil {
					if cfg.Verbose {
						fmt.Fprintf(os.Stderr, "stage2 [%s/%.40s]: %v — drop\n",
							arch.ID, prompt, callErr)
					}
					return
				}
				if !accepted {
					return
				}
				writeMu.Lock()
				err := writePairJSONL(f, pair)
				writeMu.Unlock()
				if err != nil {
					fmt.Fprintf(os.Stderr, "stage2 write %s: %v\n", arch.ID, err)
					return
				}
				n := atomic.AddInt64(&processed, 1)
				if cfg.Verbose && n%25 == 0 {
					fmt.Fprintf(os.Stderr, "stage2 progress: %d accepted\n", n)
				}
			}(av.arch, variant)
		}
	}
	wg.Wait()
	stats.StageTwoElapsed = time.Since(stage2Start)
	stats.TotalElapsed = time.Since(runStart)

	// Re-count by scanning the file (cheap; gives true total).
	final, err := countLines(cfg.OutPath)
	if err == nil {
		stats.VariantsAccepted = final - len(existing)
		stats.StageTwoCalls = int(stats.VariantsAttempted)
		stats.VariantsRejected = stats.VariantsAttempted - stats.VariantsAccepted
	}
	return stats, nil
}

func applyDefaults(cfg GenConfig) GenConfig {
	if cfg.VariantsPerArch <= 0 {
		cfg.VariantsPerArch = 30
	}
	if cfg.OutPath == "" {
		home, _ := os.UserHomeDir()
		cfg.OutPath = filepath.Join(home, ".olifant", "training", "format-v1", "pairs.jsonl")
	}
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.Model == "" {
		cfg.Model = "opus"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.PerCallTimeout <= 0 {
		cfg.PerCallTimeout = 90 * time.Second
	}
	return cfg
}

// paraphraseVariants asks Opus for N paraphrastic variants of the
// archetype's seed request, returning the variant strings as a slice.
// Stage-1 output is grammar-constrained via --output-format json + a
// JSON schema asking for {"variants": [string, ...]}.
func paraphraseVariants(ctx context.Context, cfg GenConfig, a ArchetypeDef) ([]string, error) {
	prompt := buildParaphrasePrompt(a, cfg.VariantsPerArch)
	raw, err := callClaude(ctx, cfg, prompt, paraphraseSchema(cfg.VariantsPerArch))
	if err != nil {
		return nil, fmt.Errorf("call claude: %w", err)
	}
	var out struct {
		Variants []string `json:"variants"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse variants JSON: %w (raw=%q)", err, truncStr(raw, 200))
	}
	cleaned := make([]string, 0, len(out.Variants))
	for _, v := range out.Variants {
		v = strings.TrimSpace(v)
		if v != "" {
			cleaned = append(cleaned, v)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("zero variants in response")
	}
	return cleaned, nil
}

// synthVerdict asks Opus for one verdict-YAML for the given prompt,
// validates it against schema.go's coherence rules, and retries once
// on parse/validation failure.
func synthVerdict(ctx context.Context, cfg GenConfig, a ArchetypeDef, prompt string) (Pair, bool, bool, error) {
	var lastErr error
	retried := false
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			retried = true
		}
		yamlPrompt := buildVerdictPrompt(a, prompt)
		raw, err := callClaude(ctx, cfg, yamlPrompt, nil)
		if err != nil {
			lastErr = err
			continue
		}
		yamlStr := stripYAMLFence(raw)
		doc, perr := ParseVerdictYAML([]byte(yamlStr))
		if perr != nil {
			lastErr = fmt.Errorf("parse: %w", perr)
			continue
		}
		if verr := doc.Validate(); verr != nil {
			lastErr = fmt.Errorf("validate: %w", verr)
			continue
		}
		if doc.Challenge.Verdict != a.ExpectedVerdict {
			lastErr = fmt.Errorf("verdict %q != expected %q",
				doc.Challenge.Verdict, a.ExpectedVerdict)
			continue
		}
		return Pair{
			Prompt:      prompt,
			Response:    yamlStr,
			Archetype:   a.ID,
			Verdict:     doc.Challenge.Verdict,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		}, true, retried, nil
	}
	return Pair{}, false, retried, lastErr
}

// callClaude shells out to `claude --print --model <model> -- <prompt>`,
// optionally with --output-format json + --json-schema for stage 1.
// Returns the raw stdout (JSON for --output-format json runs, free text
// otherwise) trimmed.
func callClaude(ctx context.Context, cfg GenConfig, prompt string, schema map[string]interface{}) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cfg.PerCallTimeout)
	defer cancel()

	args := []string{
		"--print",
		"--model", cfg.Model,
	}
	if schema != nil {
		schemaBytes, err := json.Marshal(schema)
		if err != nil {
			return "", fmt.Errorf("marshal schema: %w", err)
		}
		args = append(args, "--output-format", "json", "--json-schema", string(schemaBytes))
	}
	args = append(args, "--", prompt)

	cmd := exec.CommandContext(cctx, cfg.ClaudeBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude subprocess: %w (stderr=%s)", err, trimStr(stderr.String(), 200))
	}
	if schema != nil {
		// When --output-format json is set, claude wraps result in an
		// envelope. We pull the structured_output (or result) field.
		var env struct {
			Result           string          `json:"result"`
			StructuredOutput json.RawMessage `json:"structured_output"`
			IsError          bool            `json:"is_error"`
			Subtype          string          `json:"subtype"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			return "", fmt.Errorf("parse claude envelope: %w (stdout=%q)",
				err, truncStr(stdout.String(), 200))
		}
		if env.IsError {
			return "", fmt.Errorf("claude returned error (subtype=%s): %s", env.Subtype, env.Result)
		}
		if len(env.StructuredOutput) > 0 {
			return strings.TrimSpace(string(env.StructuredOutput)), nil
		}
		return strings.TrimSpace(env.Result), nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

// paraphraseSchema constrains stage-1 output to {"variants": [string × N]}.
// We use exactly N — under-supplying is acceptable (lossy archetype) but
// over-supplying wastes downstream calls.
func paraphraseSchema(n int) map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"variants"},
		"properties": map[string]interface{}{
			"variants": map[string]interface{}{
				"type":     "array",
				"minItems": 1,
				"maxItems": n,
				"items":    map[string]interface{}{"type": "string", "minLength": 5},
			},
		},
	}
}

// buildParaphrasePrompt seeds the stage-1 call.
func buildParaphrasePrompt(a ArchetypeDef, n int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are generating training data for a code-review classifier on the ElatusDev/AkademiaPlus platform.\n\n")
	fmt.Fprintf(&sb, "ARCHETYPE: %s\n", a.Description)
	fmt.Fprintf(&sb, "SEED REQUEST: %q\n", a.SeedRequest)
	fmt.Fprintf(&sb, "EXPECTED VERDICT: %s\n", a.ExpectedVerdict)
	if len(a.TargetCites) > 0 {
		fmt.Fprintf(&sb, "TARGET CITES (must remain implicated by every variant): %s\n", strings.Join(a.TargetCites, ", "))
	}
	if len(a.ScopeHint) > 0 {
		fmt.Fprintf(&sb, "SCOPE HINT: %s\n", strings.Join(a.ScopeHint, ", "))
	}
	fmt.Fprintf(&sb, "\nGenerate %d paraphrastic variants of the SEED REQUEST. Each variant MUST:\n", n)
	sb.WriteString("1. Preserve the same underlying intent so the system still picks the EXPECTED VERDICT.\n")
	sb.WriteString("2. Stay implicating the TARGET CITES if any are listed.\n")
	sb.WriteString("3. Vary phrasing (formal/informal, brief/verbose, different framing).\n")
	sb.WriteString("4. Stay under ~30 words per variant.\n")
	sb.WriteString("5. NOT include any code blocks or YAML — just the request text.\n")
	sb.WriteString("\nReturn JSON: {\"variants\": [\"variant 1\", \"variant 2\", ...]} with EXACTLY ")
	fmt.Fprintf(&sb, "%d entries.\n", n)
	return sb.String()
}

// buildVerdictPrompt seeds the stage-2 call.
func buildVerdictPrompt(a ArchetypeDef, prompt string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are producing a gold-truth verdict-YAML for a code-review training dataset.\n\n")
	fmt.Fprintf(&sb, "USER REQUEST: %q\n", prompt)
	fmt.Fprintf(&sb, "EXPECTED VERDICT: %s\n", a.ExpectedVerdict)
	if len(a.TargetCites) > 0 {
		fmt.Fprintf(&sb, "TARGET CITES (must appear in the verdict’s cites[] or applicable_rules): %s\n", strings.Join(a.TargetCites, ", "))
	}
	if len(a.ScopeHint) > 0 {
		fmt.Fprintf(&sb, "SCOPE: %s\n", strings.Join(a.ScopeHint, ", "))
	}
	sb.WriteString("\n--- SCHEMA ---\n")
	sb.WriteString("Top-level YAML must be:\n")
	sb.WriteString("challenge:\n")
	sb.WriteString("  request: <verbatim USER REQUEST string>\n")
	sb.WriteString("  verdict: <exactly the EXPECTED VERDICT>\n")
	sb.WriteString("  confirms: [...]   # {claim, cites: [...]}; cites required if entry present\n")
	sb.WriteString("  contradicts: [...] # {claim, counter, cites: [...]}; cites required if entry present\n")
	sb.WriteString("  clarify: [...]    # {question, why_asking}\n")
	sb.WriteString("  applicable_rules:\n")
	sb.WriteString("    standards: [...]\n")
	sb.WriteString("    patterns: [...]\n")
	sb.WriteString("    anti_patterns_to_avoid: [...]\n")
	sb.WriteString("    decisions_to_honor: [...]\n")
	sb.WriteString("  proceed: <proceed_directly | confirm_with_user | abort>\n")
	sb.WriteString("\n--- VERDICT/PROCEED MAPPING ---\n")
	sb.WriteString("VALID                -> proceed_directly\n")
	sb.WriteString("VALID_WITH_CAVEATS   -> confirm_with_user\n")
	sb.WriteString("INVALID              -> abort\n")
	sb.WriteString("NEEDS_CLARIFICATION  -> confirm_with_user\n")
	sb.WriteString("OUT_OF_SCOPE         -> abort\n")
	sb.WriteString("\n--- COHERENCE RULES (must hold) ---\n")
	sb.WriteString("- NEEDS_CLARIFICATION: clarify[] MUST have >=1 entry.\n")
	sb.WriteString("- INVALID: contradicts[] MUST have >=1 entry; each contradicts entry MUST have >=1 cite.\n")
	sb.WriteString("- VALID and VALID_WITH_CAVEATS: contradicts[] MUST be empty.\n")
	sb.WriteString("- VALID_WITH_CAVEATS: confirms[] OR clarify[] MUST have >=1 entry.\n")
	sb.WriteString("- Every confirms[] entry MUST have >=1 cite (HARD RULE 6).\n")
	sb.WriteString("\n--- CITE SHAPES (allowed) ---\n")
	sb.WriteString("Artifact IDs: D### AP## PC## FM## SB-## IV## IMF## AMS-## AWS-## ABS-## WA-... AM[CPSNHET]-## AW[CHSRTBA]-##\n")
	sb.WriteString("Fully-qualified paths starting with one of: core-api/ akademia-plus-web/ elatusdev-web/ akademia-plus-central/ akademia-plus-go/ core-api-e2e/ infra/ knowledge-base/\n")
	sb.WriteString("REJECTED cite shapes: bare filenames (README.md, CLAUDE.md), partial paths (.claude/...), display labels (chunk1), generic categories (magic_strings, owasp_top10).\n")
	sb.WriteString("\nOutput ONLY the YAML. No markdown code fences. No surrounding text.\n")
	return sb.String()
}

// stripYAMLFence removes leading/trailing ```yaml fences if the model
// emitted them despite the instruction to omit.
func stripYAMLFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```yaml\n")
	s = strings.TrimPrefix(s, "```yml\n")
	s = strings.TrimPrefix(s, "```\n")
	s = strings.TrimSuffix(s, "\n```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func writePairJSONL(f *os.File, p Pair) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = f.Write(body)
	return err
}

func readExistingPrompts(path string) (map[string]bool, error) {
	set := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return nil, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scan.Scan() {
		var p Pair
		if err := json.Unmarshal(scan.Bytes(), &p); err != nil {
			continue
		}
		if p.Prompt != "" {
			set[p.Prompt] = true
		}
	}
	return set, scan.Err()
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	n := 0
	for scan.Scan() {
		n++
	}
	return n, scan.Err()
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func trimStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ParsePairLine is exported for test ergonomics — round-trip a JSONL row.
func ParsePairLine(line []byte) (Pair, error) {
	var p Pair
	err := json.Unmarshal(line, &p)
	return p, err
}

// MarshalPair is exported for test ergonomics.
func MarshalPair(p Pair) ([]byte, error) {
	return yaml.Marshal(p)
}
