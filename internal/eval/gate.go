// Eval regression gate (#16, workflow olifant-eval-gate-v1): judges a run's
// report against count-based thresholds (D-EG1 — never verdict labels),
// writes HEAD-keyed gate-pass receipts whose staleness is detectable from
// suite + corpus fingerprints (IA4), and resolves the comparison baseline
// from the receipt log.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ElatusDev/olifant/internal/kbtree"
	"gopkg.in/yaml.v3"
)

// GateConfig holds the count thresholds (D-EG1, amended at E4: a first-try
// floor catches retry-masked regressions — count-invisible bugs like AP103
// halve the first-try rate while retries keep final counts pristine).
type GateConfig struct {
	MinClean    int     // minimum clean cases (default 11 of 12)
	MaxBlockers int     // maximum total BLOCKERs (default 0)
	MinFirstTry float64 // minimum first-try pass rate, 0..1 (0 disables; default 0.70)
}

// GateVerdict is the outcome of judging one report.
type GateVerdict struct {
	Pass        bool
	Reasons     []string // human-readable failure reasons (empty on pass)
	NewBlockers []string // case IDs blocked now but clean in the baseline
}

// Gate judges report against cfg, plus the no-new-blocker rule when a
// baseline report is supplied (nil baseline skips that check).
func Gate(report, baseline *Report, cfg GateConfig) GateVerdict {
	var v GateVerdict
	if report.CleanCases < cfg.MinClean {
		v.Reasons = append(v.Reasons,
			fmt.Sprintf("clean cases %d/%d below threshold %d", report.CleanCases, report.TotalCases, cfg.MinClean))
	}
	if report.TotalBlockers > cfg.MaxBlockers {
		v.Reasons = append(v.Reasons,
			fmt.Sprintf("total BLOCKERs %d above threshold %d", report.TotalBlockers, cfg.MaxBlockers))
	}
	if cfg.MinFirstTry > 0 && report.FirstTryPassRate < cfg.MinFirstTry {
		v.Reasons = append(v.Reasons,
			fmt.Sprintf("first-try pass rate %.0f%% below threshold %.0f%% (retry-masked regression — see AP103)",
				report.FirstTryPassRate*100, cfg.MinFirstTry*100))
	}
	for _, c := range report.Cases {
		if c.Error != "" {
			v.Reasons = append(v.Reasons, fmt.Sprintf("case %s ERRORed: %s", c.CaseID, c.Error))
		}
	}
	if baseline != nil {
		cleanInBaseline := map[string]bool{}
		for _, c := range baseline.Cases {
			if c.Error == "" && c.Blockers == 0 && c.Warnings == 0 {
				cleanInBaseline[c.CaseID] = true
			}
		}
		for _, c := range report.Cases {
			if c.Blockers > 0 && cleanInBaseline[c.CaseID] {
				v.NewBlockers = append(v.NewBlockers, c.CaseID)
			}
		}
		sort.Strings(v.NewBlockers)
		if len(v.NewBlockers) > 0 {
			v.Reasons = append(v.Reasons,
				fmt.Sprintf("new BLOCKERs on previously-clean cases: %s", strings.Join(v.NewBlockers, ", ")))
		}
	}
	v.Pass = len(v.Reasons) == 0
	return v
}

// FirstAttemptBlockerReport renders the per-case attempt-1 BLOCKER lines
// for any case whose first attempt failed (EG-F3). Empty when no case in
// the report has retry-masked BLOCKERs, which is the steady-state case on
// a healthy run. The lines name code, location, and value so a gate FAIL
// caused by a retry-masked regression self-diagnoses without needing a
// single-case `challenge -v` repro (the AP103 detection workflow).
func FirstAttemptBlockerReport(report *Report) string {
	var sb strings.Builder
	for _, c := range report.Cases {
		var blockers []FirstAttemptViolation
		for _, v := range c.FirstAttemptViolations {
			if v.Severity == "BLOCKER" {
				blockers = append(blockers, v)
			}
		}
		if len(blockers) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s (attempts=%d):\n", c.CaseID, c.Attempts)
		for _, v := range blockers {
			fmt.Fprintf(&sb, "  [%s] %s @ %s", v.Code, v.Note, v.Location)
			if v.Value != "" {
				fmt.Fprintf(&sb, " (%q)", v.Value)
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// DiffTable renders a per-case comparison of report vs baseline for the
// FAIL path (E1's actionable summary). Baseline may be nil.
func DiffTable(report, baseline *Report) string {
	base := map[string]CaseResult{}
	if baseline != nil {
		for _, c := range baseline.Cases {
			base[c.CaseID] = c
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-46s %-10s %-10s\n", "case", "now B/W", "baseline B/W")
	for _, c := range report.Cases {
		now := fmt.Sprintf("%d/%d", c.Blockers, c.Warnings)
		if c.Error != "" {
			now = "ERROR"
		}
		was := "—"
		if b, ok := base[c.CaseID]; ok {
			was = fmt.Sprintf("%d/%d", b.Blockers, b.Warnings)
		}
		fmt.Fprintf(&sb, "%-46s %-10s %-10s\n", c.CaseID, now, was)
	}
	return sb.String()
}

// LoadReport reads <runDir>/report.yaml.
func LoadReport(runDir string) (*Report, error) {
	raw, err := os.ReadFile(filepath.Join(runDir, "report.yaml"))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := yaml.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse %s/report.yaml: %w", runDir, err)
	}
	return &r, nil
}

// Receipt records one gate evaluation. Staleness is detected by comparing
// GitSHA + SuiteSHA + CorpusSHA + RepoSHA against the current state (IA4).
// SuiteID is the suite's stable identity (D-RG2): SuiteSHA answers "is this
// receipt fresh", SuiteID answers "whose lineage is this" — baseline lookups
// filter by SuiteID so a PASS from one suite is never another suite's
// baseline. Pre-HV-F1 lines carry no suite_id and never match a SuiteID
// filter. RepoSHA (olifant#82, GD-1b) fingerprints the code-family manifest:
// pre-#82 receipts carry none and go STALE-by-shape once the manifest exists;
// while no manifest exists the field is unconstrained (same best-effort
// semantics as CorpusSHA).
type Receipt struct {
	Verdict        string `json:"verdict" yaml:"verdict"` // PASS | FAIL | SKIPPED | OVERRIDE
	SuiteID        string `json:"suite_id,omitempty" yaml:"suite_id,omitempty"`
	GitSHA         string `json:"git_sha" yaml:"git_sha"`
	SuiteSHA       string `json:"suite_sha256" yaml:"suite_sha256"`
	CorpusSHA      string `json:"corpus_manifest_sha256" yaml:"corpus_manifest_sha256"`
	RepoSHA        string `json:"repo_manifest_sha256,omitempty" yaml:"repo_manifest_sha256,omitempty"`
	RunID          string `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	RunDir         string `json:"run_dir,omitempty" yaml:"run_dir,omitempty"`
	CleanCases     int    `json:"clean_cases" yaml:"clean_cases"`
	TotalCases     int    `json:"total_cases" yaml:"total_cases"`
	TotalBlockers  int    `json:"total_blockers" yaml:"total_blockers"`
	OverrideReason string `json:"override_reason,omitempty" yaml:"override_reason,omitempty"`
	Timestamp      string `json:"timestamp" yaml:"timestamp"` // RFC3339 UTC
}

// WriteReceipt writes gate-pass.yaml into runDir (when runDir != "") and
// appends one JSON line to logPath (creating parent dirs as needed).
func WriteReceipt(runDir, logPath string, r Receipt) error {
	if runDir != "" {
		body, err := yaml.Marshal(r)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(runDir, "gate-pass.yaml"), body, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// LatestReceipt returns the newest receipt in logPath matching every
// non-empty filter field (Verdict, SuiteID, GitSHA, SuiteSHA, CorpusSHA,
// RepoSHA). Returns (nil, nil) when no entry matches or the log does not
// exist.
func LatestReceipt(logPath string, filter Receipt) (*Receipt, error) {
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var r Receipt
		if err := json.Unmarshal([]byte(lines[i]), &r); err != nil {
			continue // tolerate a corrupt line; keep scanning
		}
		if filter.Verdict != "" && r.Verdict != filter.Verdict {
			continue
		}
		if filter.SuiteID != "" && r.SuiteID != filter.SuiteID {
			continue
		}
		if filter.GitSHA != "" && r.GitSHA != filter.GitSHA {
			continue
		}
		if filter.SuiteSHA != "" && r.SuiteSHA != filter.SuiteSHA {
			continue
		}
		if filter.CorpusSHA != "" && r.CorpusSHA != filter.CorpusSHA {
			continue
		}
		if filter.RepoSHA != "" && r.RepoSHA != filter.RepoSHA {
			continue
		}
		return &r, nil
	}
	return nil, nil
}

// FileSHA256 returns the hex SHA-256 of a file's contents. Missing files
// hash to the empty string with no error so optional fingerprints (e.g. a
// corpus manifest that does not exist yet) degrade gracefully.
func FileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// TreeSHA256 is FileSHA256 over a kbtree.Tree: the hex SHA-256 of the
// KB-relative file's bytes, with a missing file hashing to the empty string
// with no error (the same optional-fingerprint degrade). Identical content
// yields the identical digest in both, so receipts minted from a git ref's
// blobs are indistinguishable from worktree-minted ones (olifant#95 AC3).
func TreeSHA256(kb kbtree.Tree, rel string) (string, error) {
	if !kb.Exists(rel) {
		return "", nil
	}
	raw, err := kb.ReadFile(rel)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
