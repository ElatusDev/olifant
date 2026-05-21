package corpus

// Axes for the v2 curriculum scan (symbols + sentences). Per
// olifant-fine-tune-v2-corpus-curriculum-workflow.md §4 D-CC1, each
// item carries tags across multiple INDEPENDENT axes (e.g. an item can
// be kind=annotation AND concern=[security, persistence] AND
// scope=backend simultaneously). Axis names are used as keys in
// Symbol.Tags / Sentence.Tags. Per-axis value enums below.
//
// Co-exists with the v1 corpus types (Chunk, ChunkMetadata, Manifest)
// defined in types.go — both subsystems share this package.

// Axis names — used as keys in tag maps.
const (
	AxisLanguage     = "language"
	AxisKind         = "kind"
	AxisScope        = "scope"
	AxisConcern      = "concern"
	AxisStability    = "stability"
	AxisIDFamily     = "id_family"
	AxisSyntactic    = "syntactic_form" // sentence-only
	AxisSemanticRole = "semantic_role"  // sentence-only
	AxisModality     = "modality"       // sentence-only
	AxisSubjectRef   = "subject_ref"    // sentence-only; list of symbol IDs
)

// Language enum — single-valued.
const (
	LangJava       = "java"
	LangTypeScript = "typescript"
	LangHCL        = "hcl"
	LangJSON       = "json"
	LangYAML       = "yaml"
	LangMarkdown   = "markdown"
	LangShell      = "shell"
)

// Kind enum — single-valued. Covers identifier kinds across all languages.
const (
	KindPackage    = "package"
	KindClass      = "class"
	KindInterface  = "interface"
	KindType       = "type"
	KindFunction   = "function"
	KindMethod     = "method"
	KindField      = "field"
	KindAnnotation = "annotation"
	KindDecorator  = "decorator"
	KindConstant   = "constant"
	KindEnum       = "enum"
	KindConfigKey  = "config_key"
	KindResource   = "resource" // HCL terraform resource
	KindFile       = "file"
	KindID         = "id"     // KB cross-reference IDs (D17, AP3, etc.)
	KindAcronym    = "acronym"
)

// Scope constants live in scope.go — already defined for the v1 corpus
// subsystem (ScopeUniversal, ScopeBackend, ScopeWebapp, ScopeMobile,
// ScopeE2E, ScopeInfra, ScopePlatformProcess). The v2 curriculum
// re-uses them verbatim; no redeclaration here.

// Concern enum — multi-valued (a symbol can be both security AND persistence).
const (
	ConcernSecurity      = "security"
	ConcernPersistence   = "persistence"
	ConcernUI            = "ui"
	ConcernAPIContract   = "api-contract"
	ConcernTesting       = "testing"
	ConcernBuild         = "build"
	ConcernCI            = "ci"
	ConcernObservability = "observability"
	ConcernPerformance   = "performance"
	ConcernTenancy       = "tenancy"
)

// Stability enum — single-valued.
const (
	StabilityPublicAPI     = "public-api"
	StabilityInternalAPI   = "internal-api"
	StabilityModulePrivate = "module-private"
	StabilityTestOnly      = "test-only"
	StabilityDeprecated    = "deprecated"
)

// IDFamily enum — single-valued. Tags KB cross-reference IDs by family.
const (
	IDFamilyDecision        = "D"
	IDFamilyAntiPattern     = "AP"
	IDFamilyPattern         = "PC"
	IDFamilyFailureMode     = "FM"
	IDFamilySymbol          = "SB"
	IDFamilyInputValidation = "IV"
	IDFamilyImmutableFields = "IMF"
	IDFamilyWebappArch      = "WA"
	IDFamilyMobileAP        = "AM"
	IDFamilyWebappAP        = "AW"
	IDFamilyBackendAP       = "AB"
)

// Syntactic form enum (sentences) — single-valued.
const (
	SyntAffirmation = "affirmation"
	SyntNegation    = "negation"
	SyntQuestion    = "question"
	SyntImperative  = "imperative"
	SyntConditional = "conditional"
)

// Semantic role enum (sentences) — single-valued, LLM-classified.
const (
	RoleDefinition       = "definition"
	RoleConstraint       = "constraint"
	RoleRecommendation   = "recommendation"
	RoleAntiPattern      = "anti-pattern"
	RoleExample          = "example"
	RoleRetroNarrative   = "retro-narrative"
	RoleDecisionRationale = "decision-rationale"
	RoleCitation         = "citation"
)

// Modality enum (sentences) — single-valued, rule-based.
const (
	ModalMandatory   = "mandatory"   // MUST / required / HARD RULE
	ModalForbidden   = "forbidden"   // MUST NOT / never / forbidden
	ModalRecommended = "recommended" // should / preferred
	ModalAllowed     = "allowed"     // may / can
	ModalConditional = "conditional" // must X unless Y
)
