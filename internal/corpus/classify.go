package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ClassifyConfig drives Day-5 Phase 2 LLM classification of prose
// sentences via the `claude` CLI subprocess (HARD RULE — no SDK/HTTP).
type ClassifyConfig struct {
	InputPath string // path to a prose YAML (list of Sentence)
	BatchSize int    // sentences per `claude --print` invocation (50-100)
	Model     string // e.g. "haiku" / "claude-haiku-4-5"
	Verbose   bool
	DryRun    bool // build batches + show stats; skip subprocess + write
}

// ClassifyStats summarises one Classify() run.
type ClassifyStats struct {
	InputSentences   int           `yaml:"input_sentences"`
	BatchesAttempted int           `yaml:"batches_attempted"`
	BatchesOK        int           `yaml:"batches_ok"`
	BatchesFailed    int           `yaml:"batches_failed"`
	ClassifiedCount  int           `yaml:"classified_count"`
	Elapsed          time.Duration `yaml:"elapsed_ms"`
}

// classifiedEntry mirrors the JSON returned by the LLM for one sentence.
type classifiedEntry struct {
	ID           string   `json:"id"`
	SemanticRole string   `json:"semantic_role"`
	Concern      []string `json:"concern"`
}

// claudeJSONOutput is the envelope `claude --print --output-format json`
// returns. We only need the `result` field (the model's text response).
type claudeJSONOutput struct {
	Result string `json:"result"`
}

// Classify reads InputPath (a prose YAML), groups un-classified
// sentences into batches, and shells out to `claude --print` per batch
// to populate semantic_role + concern tags. Sentences that already
// have semantic_role are skipped (resumable). Updated YAML is written
// back in place.
func Classify(cfg ClassifyConfig) (ClassifyStats, error) {
	started := time.Now()
	stats := ClassifyStats{}

	if cfg.InputPath == "" {
		return stats, fmt.Errorf("classify: InputPath required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Model == "" {
		cfg.Model = "haiku"
	}

	raw, err := os.ReadFile(cfg.InputPath)
	if err != nil {
		return stats, fmt.Errorf("classify: read %s: %w", cfg.InputPath, err)
	}
	var sentences []Sentence
	if err := yaml.Unmarshal(raw, &sentences); err != nil {
		return stats, fmt.Errorf("classify: parse yaml: %w", err)
	}
	stats.InputSentences = len(sentences)

	// Build the to-classify queue: skip sentences that already have
	// semantic_role (resumable across partial runs).
	idx := map[string]int{}
	var queue []Sentence
	for i, s := range sentences {
		idx[s.ID] = i
		if _, has := s.Tags[AxisSemanticRole]; has {
			continue
		}
		queue = append(queue, s)
	}

	if cfg.Verbose {
		fmt.Printf("  %d input sentences, %d already classified, %d to do\n",
			stats.InputSentences, stats.InputSentences-len(queue), len(queue))
	}
	if cfg.DryRun {
		stats.BatchesAttempted = (len(queue) + cfg.BatchSize - 1) / cfg.BatchSize
		stats.Elapsed = time.Since(started)
		return stats, nil
	}

	// Process in batches.
	for start := 0; start < len(queue); start += cfg.BatchSize {
		end := start + cfg.BatchSize
		if end > len(queue) {
			end = len(queue)
		}
		batch := queue[start:end]
		stats.BatchesAttempted++

		entries, err := classifyBatch(batch, cfg)
		if err != nil {
			stats.BatchesFailed++
			if cfg.Verbose {
				fmt.Printf("  WARN batch %d-%d: %v\n", start, end, err)
			}
			continue
		}
		stats.BatchesOK++

		// Apply results back to the sentences slice by ID.
		applied := 0
		for _, e := range entries {
			pos, ok := idx[e.ID]
			if !ok {
				continue
			}
			s := &sentences[pos]
			if s.Tags == nil {
				s.Tags = map[string]any{}
			}
			if e.SemanticRole != "" {
				s.Tags[AxisSemanticRole] = e.SemanticRole
			}
			if len(e.Concern) > 0 {
				// Merge LLM-derived concern into any rule-based concern.
				s.Tags[AxisConcern] = mergeConcerns(s.Tags[AxisConcern], e.Concern)
			}
			applied++
		}
		stats.ClassifiedCount += applied

		// Persist after every batch so partial runs are durable.
		if err := writeProseYAML(cfg.InputPath, sentences); err != nil {
			return stats, fmt.Errorf("classify: persist after batch %d: %w", start, err)
		}
		if cfg.Verbose {
			fmt.Printf("  batch %d-%d: %d classified (%d total)\n",
				start, end, applied, stats.ClassifiedCount)
		}
	}

	stats.Elapsed = time.Since(started)
	return stats, nil
}

// classifyBatch invokes `claude --print` for one batch of sentences,
// parses the wrapped JSON, and returns the per-sentence classifications.
func classifyBatch(batch []Sentence, cfg ClassifyConfig) ([]classifiedEntry, error) {
	// Build the LLM input — array of {id, text} as compact JSON.
	type batchItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	items := make([]batchItem, 0, len(batch))
	for _, s := range batch {
		items = append(items, batchItem{ID: s.ID, Text: s.Text})
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("marshal items: %w", err)
	}

	prompt := classifyPrompt + "\n\nSentences:\n" + string(itemsJSON)

	// NOTE: deliberately NOT using --bare (breaks OAuth/keychain
	// auth) and NOT using --json-schema (forces tool-call mode where
	// the structured data lands outside the result field — narration
	// only in result). Prompt-driven JSON output + lenient extraction
	// is more robust here.
	cmd := exec.Command(
		"claude", "--print",
		"--model", cfg.Model,
		"--output-format", "json",
		"--no-session-persistence",
	)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exec: %v (stderr: %s)", err, stderr.String())
	}

	var env claudeJSONOutput
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return nil, fmt.Errorf("decode claude json envelope: %w (body: %s)", err, stdout.String())
	}
	if env.Result == "" {
		return nil, fmt.Errorf("empty result field in claude json")
	}

	// The model may wrap the JSON object in ```json fences or include
	// preamble. Strip to the first '{' / last '}' and parse the
	// wrapped {classifications:[...]} shape.
	body := extractJSONObject(env.Result)
	var wrapper struct {
		Classifications []classifiedEntry `json:"classifications"`
	}
	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		return nil, fmt.Errorf("decode classification wrapper: %w (body: %s)", err, body)
	}
	return wrapper.Classifications, nil
}

// extractJSONObject pulls the outermost JSON object out of a model
// response that may include preamble or markdown fences.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

// mergeConcerns combines rule-based concerns (from path heuristics)
// with LLM-derived concerns, deduping. Result is []string for YAML.
func mergeConcerns(existing any, llm []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	if cs, ok := existing.([]string); ok {
		for _, c := range cs {
			add(c)
		}
	} else if cs, ok := existing.([]any); ok {
		for _, c := range cs {
			if str, ok := c.(string); ok {
				add(str)
			}
		}
	}
	for _, c := range llm {
		add(c)
	}
	return out
}

// writeProseYAML serialises a Sentence list back to its YAML file.
func writeProseYAML(path string, sentences []Sentence) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prose-tmp-*.yaml")
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)
	if err := enc.Encode(sentences); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := enc.Close(); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const classifyPrompt = `You are classifying sentences from a technical-documentation corpus that will train a code-aware language model.

For each sentence, output a JSON object with:
  - id: echo the sentence id exactly
  - semantic_role: ONE of [definition, constraint, recommendation, anti-pattern, example, retro-narrative, decision-rationale, citation]
  - concern: ARRAY of ZERO OR MORE of [security, persistence, ui, api-contract, testing, build, ci, observability, performance, tenancy]

Definitions:
  definition          — defines a concept, term, or component
  constraint          — a hard rule, requirement, or limitation (often MUST/MUST NOT)
  recommendation      — a suggested practice (often SHOULD/RECOMMEND)
  anti-pattern        — a thing to AVOID; describes a bad practice or its consequences
  example             — concrete example or scenario
  retro-narrative     — past tense narration of what happened, outcomes, lessons
  decision-rationale  — explains WHY a decision was made
  citation            — references an external doc, link, or another KB entry

Concerns are domain-level tags; pick all that apply. Empty array if none.

Output: a JSON object {"classifications": [...]} containing the array, one entry per sentence in the same order. NO prose, NO markdown fences, just JSON.`
