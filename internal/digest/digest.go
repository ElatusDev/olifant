// Package digest generates cite-gated local-model summaries of single
// artifacts (charter R6 v1, D-DG1..6). The model drafts, deterministic code
// judges: a digest that fails the structural asserts or the cite gate is
// never cached and never emitted. The cache lives outside every corpus walk
// (D-DG3) so a digest can never become retrievable truth (D-BK9).
package digest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ElatusDev/olifant/internal/promptgate"
	"github.com/ElatusDev/olifant/internal/synth"
)

// maxInputBytes caps v1 single-shot digestion; larger sources need the
// chunked-digest follow-up (R6), not a silently truncated summary.
const maxInputBytes = 96 * 1024

// minBodyBytes is the structural floor below which a generation attempt is
// judged degenerate and retried.
const minBodyBytes = 120

// Config drives one digest run.
type Config struct {
	SourcePath  string // absolute path to the artifact
	SourceRel   string // platform-relative display/cite path (footer provenance)
	CacheDir    string // e.g. ~/.olifant/digests — must be outside the KB
	Refresh     bool   // bypass the cache
	Model       string
	MaxTokens   int
	Temperature float64
	Synth       synth.Client         // required unless the cache satisfies the run
	Resolver    *promptgate.Resolver // required unless the cache satisfies the run
}

// Result reports one digest run, cache hits included.
type Result struct {
	SourceRel string
	SourceSHA string // 12-hex prefix of the source content SHA-256
	BytesIn   int
	BytesOut  int
	Ratio     float64 // BytesOut / BytesIn
	CacheHit  bool
	Attempts  int
	Digest    string
	CachePath string
}

const systemPrompt = `You compress one platform document into a short, faithful digest for an engineer who needs its substance without reading it. Rules:
- Markdown, at most ~1/10 of the source length: a one-paragraph purpose, then the load-bearing points as terse bullets.
- Preserve artifact references EXACTLY as they appear in the source (decision IDs like D123, anti-pattern IDs like AP12, rule IDs, file paths). NEVER introduce a reference that does not appear verbatim in the source.
- No preamble, no meta-commentary, no advice beyond the source's own content.`

// Run executes the digest pipeline: cache check → generate → structural
// asserts → cite gate → cache. One regeneration attempt with feedback; a
// second failure returns an error and nothing is cached or emitted (D-DG2).
func Run(ctx context.Context, cfg Config) (*Result, error) {
	raw, err := os.ReadFile(cfg.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("digest: read source: %w", err)
	}
	if len(raw) > maxInputBytes {
		return nil, fmt.Errorf("digest: source is %d bytes, above the v1 cap (%d) — chunked digestion is a follow-up, not a silent truncation", len(raw), maxInputBytes)
	}
	sum := sha256.Sum256(raw)
	srcSHA := hex.EncodeToString(sum[:])[:12]

	keyHash := sha256.Sum256([]byte(cfg.SourceRel))
	cachePath := filepath.Join(cfg.CacheDir, hex.EncodeToString(keyHash[:])[:16]+"-"+srcSHA+".md")

	res := &Result{SourceRel: cfg.SourceRel, SourceSHA: srcSHA, BytesIn: len(raw), CachePath: cachePath}

	if !cfg.Refresh {
		if cached, cerr := os.ReadFile(cachePath); cerr == nil {
			res.Digest = string(cached)
			res.BytesOut = len(cached)
			res.Ratio = ratio(len(cached), len(raw))
			res.CacheHit = true
			return res, nil
		}
	}

	if cfg.Synth == nil || cfg.Resolver == nil {
		return nil, errors.New("digest: synthesizer and resolver are required — no unvalidated digests (D-DG2)")
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("digest: cache dir: %w", err)
	}

	feedback := ""
	lastReason := ""
	for attempt := 1; attempt <= 2; attempt++ {
		res.Attempts = attempt
		userPrompt := string(raw)
		if feedback != "" {
			userPrompt = "PREVIOUS ATTEMPT REJECTED: " + feedback + "\n\nDocument to digest:\n\n" + userPrompt
		} else {
			userPrompt = "Document to digest:\n\n" + userPrompt
		}
		resp, gerr := cfg.Synth.Generate(ctx, synth.Request{
			Model:       cfg.Model,
			System:      systemPrompt,
			Prompt:      userPrompt,
			Temperature: cfg.Temperature,
			MaxTokens:   cfg.MaxTokens,
		})
		if gerr != nil {
			return nil, fmt.Errorf("digest: generate: %w", gerr)
		}

		body := strings.TrimSpace(resp.Text)
		if len(body) < minBodyBytes {
			lastReason = fmt.Sprintf("degenerate body (%d bytes)", len(body))
			feedback = "the digest was empty or far too short; produce the purpose paragraph plus the load-bearing bullets"
			continue
		}
		doc := body + footer(cfg.SourceRel, srcSHA, cfg.Model)

		tmp := cachePath + ".tmp"
		if werr := os.WriteFile(tmp, []byte(doc), 0o644); werr != nil {
			return nil, fmt.Errorf("digest: write tmp: %w", werr)
		}
		rep, cerr := cfg.Resolver.CheckDoc(tmp)
		if cerr != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("digest: cite gate: %w", cerr)
		}
		if rep.Unresolved > 0 {
			var bad []string
			for _, it := range rep.Items {
				if it.Verdict == promptgate.VerdictUnresolved {
					bad = append(bad, it.Cite)
				}
			}
			_ = os.Remove(tmp)
			lastReason = fmt.Sprintf("%d unresolved cite(s): %s", rep.Unresolved, strings.Join(bad, ", "))
			feedback = "these references did not resolve against the knowledge base: " + strings.Join(bad, ", ") +
				" — only keep references that appear verbatim in the source document"
			continue
		}
		if rerr := os.Rename(tmp, cachePath); rerr != nil {
			return nil, fmt.Errorf("digest: cache write: %w", rerr)
		}
		res.Digest = doc
		res.BytesOut = len(doc)
		res.Ratio = ratio(len(doc), len(raw))
		return res, nil
	}
	return nil, fmt.Errorf("digest: rejected after %d attempts — %s (nothing cached or emitted)", res.Attempts, lastReason)
}

// footer is code-authored provenance (never demanded from the model): the
// source path doubles as a self-validating path cite for the gate.
func footer(sourceRel, srcSHA, model string) string {
	return fmt.Sprintf("\n\n---\nsource: %s\nsource_sha: %s\ngenerated_by: olifant digest (%s)\n", sourceRel, srcSHA, model)
}

func ratio(out, in int) float64 {
	if in == 0 {
		return 0
	}
	return float64(out) / float64(in)
}
