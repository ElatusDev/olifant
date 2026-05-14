package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/config"
	"gopkg.in/yaml.v3"
)

// RunConfig drives one suite execution.
type RunConfig struct {
	Suite        *Suite
	PlatformRoot string // for relative file paths
	KBRoot       string // for short-term writes + validator load
	OutDir       string // where to write run results
	Verbose      bool
}

// LoadSuite reads a suite YAML from disk.
func LoadSuite(path string) (*Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.SuiteID == "" {
		return nil, fmt.Errorf("suite_id is required")
	}
	if len(s.Cases) == 0 {
		return nil, fmt.Errorf("suite has no cases")
	}
	return &s, nil
}

// Run executes every case in the suite sequentially, writes per-case output
// files + meta + an aggregate report under <OutDir>/<run_id>/.
func Run(ctx context.Context, cfg RunConfig) (*Report, error) {
	startedAt := time.Now()
	runID := NewRunID(startedAt, cfg.Suite.SuiteID)
	runDir := filepath.Join(cfg.OutDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", runDir, err)
	}

	// Resolve runtime endpoints + validator once.
	rt := config.Resolve()
	validator, vErr := challenge.NewCiteValidator(cfg.PlatformRoot, cfg.KBRoot)
	if vErr != nil {
		fmt.Fprintf(os.Stderr, "eval: validator init failed (%v) — proceeding without\n", vErr)
		validator = nil
	}
	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "eval: %s suite=%s cases=%d\n", runID, cfg.Suite.SuiteID, len(cfg.Suite.Cases))
		if validator != nil {
			fmt.Fprintf(os.Stderr, "      validator terms loaded: %d\n", validator.KnownCount())
		}
	}

	report := &Report{
		RunID:      runID,
		SuiteID:    cfg.Suite.SuiteID,
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		TotalCases: len(cfg.Suite.Cases),
	}

	var gradedTotal, gradedPass int

	for i, c := range cfg.Suite.Cases {
		caseStart := time.Now()
		caseDir := filepath.Join(runDir, c.ID)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", caseDir, err)
		}

		topN := pickInt(c.TopN, cfg.Suite.Default.TopN, 6)
		maxTokens := pickInt(c.MaxTokens, cfg.Suite.Default.MaxTokens, 700)
		timeoutSec := pickInt(c.TimeoutSec, cfg.Suite.Default.TimeoutSec, 240)
		synth := pickStr(c.Synth, cfg.Suite.Default.Synth, rt.Synthesizer)

		// Build request: either --file content or literal request
		request, rerr := buildRequestForCase(c, cfg.PlatformRoot)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "  [case %d/%d] %s — request build failed: %v\n", i+1, len(cfg.Suite.Cases), c.ID, rerr)
			report.Cases = append(report.Cases, CaseResult{
				CaseID: c.ID, Scope: c.Scope, File: c.File,
				Error: rerr.Error(),
			})
			continue
		}

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "  [case %d/%d] %s [scope=%v] — running…\n",
				i+1, len(cfg.Suite.Cases), c.ID, c.Scope)
		}

		caseCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		res, runErr := challenge.Run(caseCtx, challenge.Config{
			Request:            request,
			OllamaURL:          rt.OllamaURL,
			ChromaURL:          rt.ChromaURL,
			Embedder:           rt.Embedder,
			Synthesizer:        synth,
			Tenant:             rt.ChromaTenant,
			Database:           rt.ChromaDatabase,
			Scopes:             c.Scope,
			TopN:               topN,
			Temperature:        0,
			MaxTokens:          maxTokens,
			Verbose:            false,
			Validator:          validator,
			MaxValidateRetries: 1,
		})
		cancel()

		result := CaseResult{
			CaseID:         c.ID,
			Scope:          c.Scope,
			File:           c.File,
			ElapsedMs:      time.Since(caseStart).Milliseconds(),
		}

		if runErr != nil {
			result.Error = runErr.Error()
			report.Cases = append(report.Cases, result)
			fmt.Fprintf(os.Stderr, "    error: %v\n", runErr)
			continue
		}

		// Tally severity
		for _, v := range res.RemainingCiteViolations {
			switch v.Severity {
			case challenge.SeverityBlocker:
				result.Blockers++
			case challenge.SeverityWarning:
				result.Warnings++
			case challenge.SeverityInfo:
				result.Infos++
			}
		}
		verdict, proceed := res.ExtractVerdict()
		result.Verdict = verdict
		result.Proceed = proceed
		result.Attempts = res.CiteAttempts
		result.RetrievedCount = res.RetrievedCount
		result.EmbedMs = res.EmbedMs
		result.RetrieveMs = res.RetrieveMs
		result.SynthMs = res.SynthMs
		result.EvalTokens = res.SynthEvalCount
		result.TokensPerSec = res.SynthTokensSec

		// Persist output YAML + meta
		outYAML := filepath.Join(caseDir, "output.yaml")
		if werr := os.WriteFile(outYAML, []byte(res.YAMLOutput+"\n"), 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "    warn: write output.yaml: %v\n", werr)
		}
		result.OutputYAMLPath = outYAML

		// Persist meta (with full violation list for forensics)
		writeCaseMeta(caseDir, c, res)

		// Graded eval check
		if c.Expected != nil {
			em := evalExpected(c.Expected, &result, res)
			result.ExpectedMatch = em
			gradedTotal++
			if em.VerdictPassed && em.BlockersPassed && (em.MustCitePassed || c.Expected.MustCiteAnyOf == nil) && (em.MustNotCitePassed || c.Expected.MustNotCiteAnyOf == nil) {
				gradedPass++
			}
		}

		if result.Blockers == 0 {
			report.CleanCases++
		}
		report.TotalBlockers += result.Blockers
		report.TotalWarnings += result.Warnings
		report.TotalInfos += result.Infos
		if res.CiteAttempts == 1 {
			// not tracked in Report directly; counted via Attempts == 1
		}
		report.Cases = append(report.Cases, result)

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "    verdict=%s blockers=%d warnings=%d attempts=%d elapsed=%dms\n",
				result.Verdict, result.Blockers, result.Warnings, result.Attempts, result.ElapsedMs)
		}
	}

	endedAt := time.Now()
	report.EndedAt = endedAt.UTC().Format(time.RFC3339)
	report.ElapsedMs = endedAt.Sub(startedAt).Milliseconds()
	// FirstTryPassRate := cases with Attempts == 1 / total
	firstTryPasses := 0
	for _, c := range report.Cases {
		if c.Attempts == 1 {
			firstTryPasses++
		}
	}
	if report.TotalCases > 0 {
		report.FirstTryPassRate = float64(firstTryPasses) / float64(report.TotalCases)
	}
	if gradedTotal > 0 {
		rate := float64(gradedPass) / float64(gradedTotal)
		report.GradedPassRate = &rate
	}

	// Write report
	reportPath := filepath.Join(runDir, "report.yaml")
	if werr := writeReport(reportPath, report); werr != nil {
		return report, fmt.Errorf("write report: %w", werr)
	}
	// Also persist a copy of the suite for traceability
	suiteCopy := filepath.Join(runDir, "suite.yaml")
	if body, mErr := yaml.Marshal(cfg.Suite); mErr == nil {
		_ = os.WriteFile(suiteCopy, body, 0o644)
	}
	return report, nil
}

func pickInt(a, b, def int) int {
	if a > 0 {
		return a
	}
	if b > 0 {
		return b
	}
	return def
}
func pickStr(a, b, def string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return def
}

func buildRequestForCase(c Case, platformRoot string) (string, error) {
	if c.File != "" && c.Request != "" {
		return "", fmt.Errorf("case %s: both `file` and `request` set — pick one", c.ID)
	}
	if c.File == "" && c.Request == "" {
		return "", fmt.Errorf("case %s: neither `file` nor `request` set", c.ID)
	}
	if c.Request != "" {
		return c.Request, nil
	}
	abs := filepath.Join(platformRoot, c.File)
	body, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("case %s: read %s: %w", c.ID, abs, err)
	}
	lang := languageHintForPath(c.File)
	return fmt.Sprintf(
		"Review the following %s code for ElatusDev/AkademiaPlus platform compliance.\nFile: %s\n\n```%s\n%s\n```",
		lang, c.File, lang, strings.TrimRight(string(body), "\n")), nil
}

func languageHintForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".swift":
		return "swift"
	case ".tf", ".tfvars":
		return "terraform"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".sh", ".bash", ".zsh":
		return "shell"
	default:
		return ""
	}
}

func writeCaseMeta(caseDir string, c Case, res *challenge.Result) {
	meta := map[string]interface{}{
		"case_id":          c.ID,
		"scope":            c.Scope,
		"file":             c.File,
		"elapsed_ms":       res.Elapsed.Milliseconds(),
		"embed_ms":         res.EmbedMs,
		"retrieve_ms":      res.RetrieveMs,
		"synth_ms":         res.SynthMs,
		"eval_tokens":      res.SynthEvalCount,
		"tokens_per_sec":   res.SynthTokensSec,
		"retrieved_count":  res.RetrievedCount,
		"cite_attempts":    res.CiteAttempts,
		"remaining_violations": res.RemainingCiteViolations,
	}
	body, _ := yaml.Marshal(meta)
	_ = os.WriteFile(filepath.Join(caseDir, "meta.yaml"), body, 0o644)
}

func writeReport(path string, r *Report) error {
	header := "# Olifant eval run report\n# Schema: internal/eval/types.go Report\n\n"
	body, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(header+string(body)), 0o644)
}

func evalExpected(exp *Expected, result *CaseResult, res *challenge.Result) *ExpectedMatch {
	em := &ExpectedMatch{}
	em.VerdictPassed = (exp.Verdict == "" || exp.Verdict == result.Verdict)

	if exp.MaxBlockers != nil {
		em.BlockersPassed = (result.Blockers <= *exp.MaxBlockers)
	} else {
		em.BlockersPassed = true
	}

	output := res.RawJSON
	if exp.MustCiteAnyOf != nil {
		em.MustCitePassed = false
		for _, cite := range exp.MustCiteAnyOf {
			if strings.Contains(output, cite) {
				em.MustCitePassed = true
				break
			}
		}
	}
	if exp.MustNotCiteAnyOf != nil {
		em.MustNotCitePassed = true
		for _, cite := range exp.MustNotCiteAnyOf {
			if strings.Contains(output, cite) {
				em.MustNotCitePassed = false
				break
			}
		}
	}
	return em
}
