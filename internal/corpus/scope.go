package corpus

import (
	"path/filepath"
	"strings"
)

// Scope constants — keep aligned with CNL v1 scope tiers.
const (
	ScopeUniversal       = "universal"
	ScopeBackend         = "backend"
	ScopeWebapp          = "webapp"
	ScopeMobile          = "mobile"
	ScopeE2E             = "e2e"
	ScopeInfra           = "infra"
	ScopePlatformProcess = "platform-process"
)

// AllScopes lists every scope; used to seed empty NDJSON files even when a
// scope produced zero chunks (keeps the manifest stable).
var AllScopes = []string{
	ScopeUniversal,
	ScopeBackend,
	ScopeWebapp,
	ScopeMobile,
	ScopeE2E,
	ScopeInfra,
	ScopePlatformProcess,
}

// scopeRule maps a path prefix (relative to platform/) to a scope.
// First match wins. Order matters — more specific prefixes must come first.
type scopeRule struct {
	prefix string
	scope  string
}

// kbScopeRules covers paths under platform/knowledge-base/.
// Each rule is checked with HasPrefix on the path relative to knowledge-base/.
var kbScopeRules = []scopeRule{
	// Backend
	{"retrospectives/core-api/", ScopeBackend},
	{"workflows/core-api/", ScopeBackend},
	{"prompts/core-api/", ScopeBackend},
	{"operations/core-api/", ScopeBackend},

	// Webapp
	{"retrospectives/akademia-plus-web/", ScopeWebapp},
	{"retrospectives/elatusdev-web/", ScopeWebapp},
	{"workflows/akademia-plus-web/", ScopeWebapp},
	{"workflows/elatusdev-web/", ScopeWebapp},
	{"prompts/akademia-plus-web/", ScopeWebapp},
	{"prompts/elatusdev-web/", ScopeWebapp},
	{"operations/akademia-plus-web/", ScopeWebapp},
	{"operations/elatusdev-web/", ScopeWebapp},
	{"views/", ScopeWebapp},
	{"wireframes/", ScopeWebapp},
	{"ux/", ScopeWebapp},

	// Mobile
	{"retrospectives/akademia-plus-central/", ScopeMobile},
	{"retrospectives/akademia-plus-go/", ScopeMobile},
	{"workflows/akademia-plus-central/", ScopeMobile},
	{"workflows/akademia-plus-go/", ScopeMobile},
	{"prompts/akademia-plus-central/", ScopeMobile},
	{"prompts/akademia-plus-go/", ScopeMobile},
	{"operations/akademia-plus-central/", ScopeMobile},
	{"operations/akademia-plus-go/", ScopeMobile},

	// E2E
	{"retrospectives/core-api-e2e/", ScopeE2E},
	{"workflows/core-api-e2e/", ScopeE2E},
	{"prompts/core-api-e2e/", ScopeE2E},
	{"operations/core-api-e2e/", ScopeE2E},

	// Infra
	{"retrospectives/infra/", ScopeInfra},
	{"workflows/infra/", ScopeInfra},
	{"prompts/infra/", ScopeInfra},
	{"operations/infra/", ScopeInfra},

	// Platform-process (process meta + execution logs + skills lifecycle)
	{"execution-reports/", ScopePlatformProcess},
	{"skills/", ScopePlatformProcess},
	{"templates/", ScopePlatformProcess},

	// Universal (cross-stack catalogs + architecture + audits)
	{"standards/", ScopeUniversal},
	{"decisions/", ScopeUniversal},
	{"anti-patterns/", ScopeUniversal},
	{"patterns/", ScopeUniversal},
	{"audit-report/", ScopeUniversal},
	{"architecture/", ScopeUniversal},
	{"dsl/", ScopeUniversal},
	{"dictionary/", ScopeUniversal},
	{"corpus/", ScopeUniversal},
}

// ScopeForKBPath returns the scope for a path RELATIVE to platform/knowledge-base/.
// Returns "" if no rule matches.
func ScopeForKBPath(relPath string) string {
	p := filepath.ToSlash(relPath)
	for _, r := range kbScopeRules {
		if strings.HasPrefix(p, r.prefix) {
			return r.scope
		}
	}
	return ""
}

// ScopeForRepoClaudeMd maps a repo directory name to its scope.
func ScopeForRepoClaudeMd(repoDir string) string {
	switch repoDir {
	case "core-api":
		return ScopeBackend
	case "akademia-plus-web", "elatusdev-web":
		return ScopeWebapp
	case "akademia-plus-central", "akademia-plus-go":
		return ScopeMobile
	case "core-api-e2e":
		return ScopeE2E
	case "infra":
		return ScopeInfra
	default:
		return ""
	}
}

// docTypeForPath returns the doc_type label, derived from path structure.
func docTypeForPath(kbRelPath, ext string) string {
	p := filepath.ToSlash(kbRelPath)
	switch {
	case strings.HasPrefix(p, "standards/") && ext == ".yaml":
		return "standard"
	case strings.HasPrefix(p, "decisions/") && ext == ".yaml":
		return "decision"
	case strings.HasPrefix(p, "anti-patterns/") && ext == ".yaml":
		return "anti_pattern"
	case strings.HasPrefix(p, "patterns/"):
		return "pattern"
	case strings.HasPrefix(p, "retrospectives/"):
		return "retro"
	case strings.HasPrefix(p, "workflows/"):
		return "workflow"
	case strings.HasPrefix(p, "prompts/"):
		return "prompt"
	case strings.HasPrefix(p, "templates/"):
		return "template"
	case strings.HasPrefix(p, "skills/"):
		return "skill"
	case strings.HasPrefix(p, "audit-report/"):
		return "audit"
	case strings.HasPrefix(p, "architecture/"):
		return "architecture"
	case strings.HasPrefix(p, "operations/"):
		return "operation"
	case strings.HasPrefix(p, "ux/"), strings.HasPrefix(p, "views/"), strings.HasPrefix(p, "wireframes/"):
		return "view"
	case strings.HasPrefix(p, "execution-reports/"):
		return "execution_report"
	case strings.HasPrefix(p, "dsl/"), strings.HasPrefix(p, "dictionary/"), strings.HasPrefix(p, "corpus/"):
		return "meta"
	default:
		return "doc"
	}
}
