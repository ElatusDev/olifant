// Claude Code subprocess configuration. The Claude executor shells out
// to the `claude` CLI (Claude Code), which authenticates against the
// user's existing subscription via keychain/OAuth. No API key required.
//
// This is the ClaudeCodeSubprocessExecutor referenced in
// internal/psp/executor.go and psp-v1.md §10.
package config

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ClaudeConfig is the runtime tuning for the Claude Code subprocess executor.
//
// Env var precedence (highest first):
//
//	OLIFANT_CLAUDE_BINARY     default: first "claude" on $PATH
//	OLIFANT_CLAUDE_MODEL      default: claude-sonnet-4-6
//	OLIFANT_CLAUDE_EFFORT     default: empty (use CLI's default)
//	OLIFANT_CLAUDE_TIMEOUT    default: 120 (seconds, per step)
//	OLIFANT_CLAUDE_WORKDIR    default: empty (subprocess inherits cwd)
type ClaudeConfig struct {
	Binary  string
	Model   string
	Effort  string
	Timeout time.Duration
	WorkDir string
}

// Default model is Sonnet 4.6 — best speed/cost balance for PSP steps
// that don't need Opus-tier reasoning. Subscription quota is finite even
// on Max plans, so we don't default to Opus.
const DefaultClaudeModel = "claude-sonnet-4-6"

// DefaultClaudeTimeout caps a single subprocess invocation. PSP steps
// should be short; if a step takes more than 2 minutes the runner can
// retry with backoff (cheaper than letting a runaway subprocess block).
const DefaultClaudeTimeout = 120 * time.Second

// ResolveClaude reads ClaudeConfig from environment.
//
// Returns (cfg, ok). ok=false when the claude binary is not on PATH and
// no explicit OLIFANT_CLAUDE_BINARY override is set — caller should fall
// back to local-only execution. ok=true means the binary is callable.
func ResolveClaude() (ClaudeConfig, bool) {
	binary := env("OLIFANT_CLAUDE_BINARY", "")
	if binary == "" {
		resolved, err := exec.LookPath("claude")
		if err != nil {
			return ClaudeConfig{}, false
		}
		binary = resolved
	} else {
		// Honor an explicit override even if the path is bogus — fail at
		// invocation time with a clear error instead of silently falling
		// back to local. The empty-string default above hides this branch.
		if _, err := exec.LookPath(binary); err != nil {
			return ClaudeConfig{}, false
		}
	}
	timeoutSec := envInt("OLIFANT_CLAUDE_TIMEOUT", int(DefaultClaudeTimeout/time.Second))
	cfg := ClaudeConfig{
		Binary:  binary,
		Model:   env("OLIFANT_CLAUDE_MODEL", DefaultClaudeModel),
		Effort:  env("OLIFANT_CLAUDE_EFFORT", ""),
		Timeout: time.Duration(timeoutSec) * time.Second,
		WorkDir: env("OLIFANT_CLAUDE_WORKDIR", ""),
	}
	return cfg, true
}

// String dumps non-secret fields for logging. Subscription auth lives in
// keychain so there's nothing secret to redact at the config level.
func (c ClaudeConfig) String() string {
	wd := c.WorkDir
	if wd == "" {
		wd = "(inherit)"
	}
	effort := c.Effort
	if effort == "" {
		effort = "(default)"
	}
	return fmt.Sprintf("claude binary=%s model=%s effort=%s timeout=%s workdir=%s",
		c.Binary, c.Model, effort, c.Timeout, wd)
}

func envInt(key string, def int) int {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	}
	return def
}
