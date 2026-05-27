package embedder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GenConfig drives the Phase B1a triple-generation pipeline. Each anchor in
// Sentences is paired with the mined Negative (already in cfg.Triples) plus
// one Opus-synthesised positive paraphrase, written as one JSONL row.
type GenConfig struct {
	Triples        []Triple      // anchor + mined negative, from Mine()
	OutPath        string        // append-only JSONL output (default ~/.olifant/training/embedder-v1/triples.jsonl)
	FailuresPath   string        // append-only JSONL of per-anchor failures (default: sibling of OutPath as failures.jsonl)
	ClaudeBin      string        // default "claude"
	Model          string        // default "opus" (HARD RULE: must remain opus)
	Resume         bool          // skip anchors already on disk by AnchorID
	Limit          int           // process only first N triples (0 = all); 1000 for the §4 B1a sanity gate
	Concurrency    int           // parallel paraphrase calls (default 1 per workflow)
	PerCallTimeout time.Duration // default 60s
	MaxRetries     int           // default 1 (per workflow §4 B1a retry policy)
	Verbose        bool
}

// FailureKind classifies why a paraphrase call failed. The 2026-05-27 run
// produced 884 failures with no per-anchor diagnostic; this enum is the
// vocabulary that re-runs use to surface failure modes.
type FailureKind string

const (
	FailTimeout         FailureKind = "timeout"           // context deadline before claude returned
	FailSubprocess      FailureKind = "subprocess_error" // claude exited non-zero (or could not be exec'd)
	FailEnvelopeParse   FailureKind = "envelope_parse"   // outer claude JSON envelope malformed
	FailErrorEnvelope   FailureKind = "error_envelope"   // claude returned is_error=true
	FailParaphraseParse FailureKind = "paraphrase_parse" // structured_output JSON not {paraphrase: string}
	FailEmptyParaphrase FailureKind = "empty_paraphrase" // paraphrase field present but empty after trim
)

// ParaphraseError is the typed error returned by paraphrase() / callClaude()
// so Generate() can record a classified FailureRow.
type ParaphraseError struct {
	Kind   FailureKind
	Detail string // truncated evidence (raw output snippet, claude stderr, etc.)
}

func (e *ParaphraseError) Error() string {
	if e.Detail == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Detail
}

// FailureRow is the JSONL-on-disk schema for the failures sidecar.
type FailureRow struct {
	AnchorID    string      `json:"anchor_id"`
	Scope       string      `json:"scope"`
	AnchorRole  string      `json:"anchor_role"`
	SourcePath  string      `json:"source_path"`
	Anchor      string      `json:"anchor"`
	Kind        FailureKind `json:"kind"`
	Detail      string      `json:"detail"`
	Attempts    int         `json:"attempts"` // total tries (1 + retries)
	GeneratedAt string      `json:"generated_at"`
}

// Stats summarises one Generate() run. Includes the §4 B1a sanity-quality
// signals so the CLI can decide whether to halt before the full 7716 run.
type Stats struct {
	Anchors         int
	Processed       int
	Skipped         int                 // resume-skipped
	Succeeded       int                 // paraphrase call returned valid JSON
	Failed          int                 // paraphrase call exhausted retries
	RetriedOnce     int                 // succeeded but only after 1 retry
	MeanRatio       float64             // mean(len(paraphrase) / len(anchor))
	ArtifactIDHits  int                 // # paraphrases that retained ≥1 of anchor's artifact IDs
	ArtifactIDTotal int                 // # anchors that had any artifact ID to begin with
	FailuresByKind  map[FailureKind]int // breakdown of Failed by classified kind
	FailuresPath    string              // resolved path of the failures.jsonl sidecar (empty if none written)
	Elapsed         time.Duration
}

// PairedRow is the JSONL-on-disk schema. Designed for sentence-transformers
// `JsonDataset` loaders which read `anchor`/`positive`/`negative` and ignore
// other keys.
type PairedRow struct {
	Anchor       string `json:"anchor"`
	Positive     string `json:"positive"`
	Negative     string `json:"negative"`
	AnchorID     string `json:"anchor_id"`
	NegativeID   string `json:"negative_id"`
	Scope        string `json:"scope"`
	AnchorRole   string `json:"anchor_role"`
	NegativeRole string `json:"negative_role"`
	SourcePath   string `json:"source_path"`
	Relaxed      bool   `json:"relaxed,omitempty"`
	GeneratedAt  string `json:"generated_at"`
}

// Generate runs the Opus paraphrase loop over cfg.Triples, writing one
// JSONL row per success. Respects cfg.Resume to skip anchor IDs already
// present on disk.
func Generate(ctx context.Context, cfg GenConfig) (Stats, error) {
	if len(cfg.Triples) == 0 {
		return Stats{}, errors.New("Generate: cfg.Triples is empty")
	}
	if cfg.Model == "" {
		cfg.Model = "opus"
	}
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.PerCallTimeout <= 0 {
		cfg.PerCallTimeout = 60 * time.Second
	}

	out := cfg.OutPath
	if out == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Stats{}, fmt.Errorf("home dir: %w", err)
		}
		out = filepath.Join(home, ".olifant", "training", "embedder-v1", "triples.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return Stats{}, fmt.Errorf("mkdir out: %w", err)
	}

	failuresPath := cfg.FailuresPath
	if failuresPath == "" {
		failuresPath = filepath.Join(filepath.Dir(out), "failures.jsonl")
	}

	existing := map[string]bool{}
	if cfg.Resume {
		seen, err := loadExistingAnchorIDs(out)
		if err != nil {
			return Stats{}, fmt.Errorf("scan existing: %w", err)
		}
		existing = seen
	}

	work := cfg.Triples
	if cfg.Limit > 0 && cfg.Limit < len(work) {
		work = work[:cfg.Limit]
	}

	st := Stats{Anchors: len(work), FailuresByKind: map[FailureKind]int{}}
	start := time.Now()

	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return st, fmt.Errorf("open out: %w", err)
	}
	defer f.Close()

	// Failures sidecar — opened lazily on first failure so a clean run leaves no file.
	var (
		failuresFile    *os.File
		failuresWritten bool
	)
	defer func() {
		if failuresFile != nil {
			failuresFile.Close()
		}
	}()

	var (
		mu       sync.Mutex
		ratioSum float64
		ratioN   int
		artifHit int
		artifTot int
		writeErr error
	)

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

dispatch:
	for i, tr := range work {
		if existing[tr.AnchorID] {
			st.Skipped++
			continue
		}
		select {
		case <-ctx.Done():
			break dispatch
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, tr Triple) {
			defer wg.Done()
			defer func() { <-sem }()

			pos, attempts, err := paraphrase(ctx, cfg, tr.Anchor)
			mu.Lock()
			defer mu.Unlock()
			st.Processed++
			if err != nil {
				st.Failed++
				kind := FailureKind("unknown")
				detail := err.Error()
				var pe *ParaphraseError
				if errors.As(err, &pe) {
					kind = pe.Kind
					detail = pe.Detail
				}
				st.FailuresByKind[kind]++
				if failuresFile == nil {
					ff, ferr := os.OpenFile(failuresPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
					if ferr != nil {
						writeErr = fmt.Errorf("open failures: %w", ferr)
						return
					}
					failuresFile = ff
				}
				row := FailureRow{
					AnchorID:    tr.AnchorID,
					Scope:       tr.Scope,
					AnchorRole:  tr.AnchorRole,
					SourcePath:  tr.SourcePath,
					Anchor:      tr.Anchor,
					Kind:        kind,
					Detail:      detail,
					Attempts:    attempts,
					GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				}
				if line, jerr := json.Marshal(row); jerr != nil {
					writeErr = fmt.Errorf("marshal failure %s: %w", tr.AnchorID, jerr)
				} else if _, werr := failuresFile.Write(append(line, '\n')); werr != nil {
					writeErr = fmt.Errorf("write failure %s: %w", tr.AnchorID, werr)
				} else {
					failuresWritten = true
				}
				if cfg.Verbose {
					fmt.Fprintf(os.Stderr, "[%d/%d] anchor=%s FAILED kind=%s: %s\n",
						idx+1, len(work), tr.AnchorID, kind, truncStr(detail, 120))
				}
				return
			}
			st.Succeeded++
			if attempts > 1 {
				st.RetriedOnce++
			}

			row := PairedRow{
				Anchor:       tr.Anchor,
				Positive:     pos,
				Negative:     tr.Negative,
				AnchorID:     tr.AnchorID,
				NegativeID:   tr.NegativeID,
				Scope:        tr.Scope,
				AnchorRole:   tr.AnchorRole,
				NegativeRole: tr.NegativeRole,
				SourcePath:   tr.SourcePath,
				Relaxed:      tr.Relaxed,
				GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
			}
			line, jerr := json.Marshal(row)
			if jerr != nil {
				writeErr = fmt.Errorf("marshal row %s: %w", tr.AnchorID, jerr)
				return
			}
			if _, werr := f.Write(append(line, '\n')); werr != nil {
				writeErr = fmt.Errorf("write row %s: %w", tr.AnchorID, werr)
				return
			}

			ratio := float64(len(pos)) / float64(max1(len(tr.Anchor)))
			ratioSum += ratio
			ratioN++
			ids := anchorIDsIn(tr.Anchor)
			if len(ids) > 0 {
				artifTot++
				if anyOverlap(ids, anchorIDsIn(pos)) {
					artifHit++
				}
			}
			if cfg.Verbose {
				retryStr := ""
				if attempts > 1 {
					retryStr = " (retried)"
				}
				fmt.Fprintf(os.Stderr, "[%d/%d] anchor=%s ratio=%.2f%s\n",
					idx+1, len(work), tr.AnchorID, ratio, retryStr)
			}
		}(i, tr)
	}

	wg.Wait()
	st.Elapsed = time.Since(start)
	if ratioN > 0 {
		st.MeanRatio = ratioSum / float64(ratioN)
	}
	st.ArtifactIDHits = artifHit
	st.ArtifactIDTotal = artifTot
	if failuresWritten {
		st.FailuresPath = failuresPath
	}
	if writeErr != nil {
		return st, writeErr
	}
	return st, nil
}

// paraphrase shells out to `claude --print --model opus` with a JSON-schema-
// constrained prompt and returns the extracted paraphrase string. On failure
// the returned error is always a *ParaphraseError (kind-classified).
//
// The returned attempts count is total tries (1 + retries actually performed),
// so callers can attribute "succeeded after retry" vs "exhausted retries".
func paraphrase(ctx context.Context, cfg GenConfig, anchor string) (string, int, error) {
	prompt := buildParaphrasePrompt(anchor)
	schema := paraphraseSchema()

	attempts := 0
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		attempts = attempt + 1
		raw, err := callClaude(ctx, cfg, prompt, schema)
		if err != nil {
			lastErr = err
			continue
		}
		var out struct {
			Paraphrase string `json:"paraphrase"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			lastErr = &ParaphraseError{
				Kind:   FailParaphraseParse,
				Detail: fmt.Sprintf("%v (raw=%s)", err, truncStr(raw, 200)),
			}
			continue
		}
		p := strings.TrimSpace(out.Paraphrase)
		if p == "" {
			lastErr = &ParaphraseError{Kind: FailEmptyParaphrase, Detail: truncStr(raw, 200)}
			continue
		}
		return p, attempts, nil
	}
	return "", attempts, lastErr
}

func buildParaphrasePrompt(anchor string) string {
	return "Rewrite this sentence in different words while preserving its technical claim " +
		"and any artifact IDs verbatim (e.g., AP3, D139, SB-04, AMS-02, paths like " +
		"core-api/...). Output ONLY the JSON object: {\"paraphrase\": \"...\"}. " +
		"Sentence: " + anchor
}

func paraphraseSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"paraphrase": map[string]interface{}{"type": "string"},
		},
		"required":             []string{"paraphrase"},
		"additionalProperties": false,
	}
}

// callClaude mirrors the format-package pattern: `claude --print --model M
// --output-format json --json-schema {…} -- <prompt>`. Returns the schema-
// constrained structured_output payload as JSON string.
func callClaude(ctx context.Context, cfg GenConfig, prompt string, schema map[string]interface{}) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cfg.PerCallTimeout)
	defer cancel()

	args := []string{"--print", "--model", cfg.Model}
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
		if cctx.Err() == context.DeadlineExceeded {
			return "", &ParaphraseError{Kind: FailTimeout, Detail: truncStr(stderr.String(), 200)}
		}
		return "", &ParaphraseError{
			Kind:   FailSubprocess,
			Detail: fmt.Sprintf("%v (stderr=%s)", err, truncStr(stderr.String(), 200)),
		}
	}

	if schema == nil {
		return strings.TrimSpace(stdout.String()), nil
	}

	var env struct {
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return "", &ParaphraseError{
			Kind:   FailEnvelopeParse,
			Detail: fmt.Sprintf("%v (stdout=%s)", err, truncStr(stdout.String(), 200)),
		}
	}
	if env.IsError {
		return "", &ParaphraseError{Kind: FailErrorEnvelope, Detail: env.Subtype}
	}
	if len(env.StructuredOutput) > 0 {
		return string(env.StructuredOutput), nil
	}
	return env.Result, nil
}

// loadExistingAnchorIDs scans the JSONL output file (if it exists) for
// `anchor_id` values, so --resume can skip already-processed anchors.
func loadExistingAnchorIDs(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]bool{}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var row PairedRow
			if jerr := json.Unmarshal(bytes.TrimSpace(line), &row); jerr == nil && row.AnchorID != "" {
				out[row.AnchorID] = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// anchorIDPattern matches the artifact-ID vocabulary we ask Opus to preserve.
// Conservative — anchors only the well-known prefixes and full repo-prefixed
// paths. Used by Generate() to compute the §4 B1a sanity "% paraphrases
// containing anchor's artifact-IDs" signal.
var anchorIDPattern = regexp.MustCompile(
	`\b(?:` +
		`AP\d+|D\d+|PC\d+|CI\d+|F\d+|SB-\d+|SW-\d+|TBX-\d+|AMS-\d+|WA-W\d+|WA-[A-Z]\d?|` +
		`AWS-\d+|AWS|AMS|D-CC\d+|FM\d+|IMF\d+|AP\b` +
		`)\b`)

func anchorIDsIn(text string) []string {
	return anchorIDPattern.FindAllString(text, -1)
}

func anyOverlap(a, b []string) bool {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
