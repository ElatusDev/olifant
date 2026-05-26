package format

// ArchetypeDef is one of the ~50 query archetypes from which Phase C1
// paraphrastically generates 20-40 prompt variants apiece via Opus.
//
// Field semantics:
//   - ID: stable kebab-case identifier; used in JSONL output for forensics.
//   - Description: one-line intent; the Opus paraphrase prompt seeds with
//     this so all variants share the same underlying meaning.
//   - SeedRequest: one representative phrasing as a starting point for
//     paraphrase (Opus is asked to produce 30 alternative phrasings of
//     this same intent, preserving the expected verdict + cites).
//   - ExpectedVerdict: one of VerdictValid..VerdictOutOfScope. Hard-set
//     for the archetype — paraphrastic variation MUST NOT change it.
//   - TargetCites: cite IDs/paths the gold-truth verdict must include
//     (e.g., contradicts[].cites for INVALID, applicable_rules for
//     others). Empty for OUT_OF_SCOPE.
//   - ScopeHint: scope tag(s) — used in the verdict-YAML synthesis
//     prompt as the retrieval-context substitute (we tell Opus
//     "synthesize as if you retrieved chunks scoped to X").
//   - Notes: 1-line "why this archetype matters" — author trace, not
//     used at runtime.
type ArchetypeDef struct {
	ID              string
	Description     string
	SeedRequest     string
	ExpectedVerdict string
	TargetCites     []string
	ScopeHint       []string
	Notes           string
}

// Archetypes returns the ~50 hand-crafted archetypes spanning all five
// verdicts across the seven platform scopes. Coverage targets:
//
//	VALID                10  — well-aligned proposals (1-2 per scope)
//	VALID_WITH_CAVEATS   10  — aligned with notable caveats
//	INVALID              15  — clear AP/decision/rule violations
//	NEEDS_CLARIFICATION  10  — under-specified / ambiguous
//	OUT_OF_SCOPE          5  — off-platform topics
func Archetypes() []ArchetypeDef {
	return []ArchetypeDef{
		// ===== VALID (10) =====
		{
			ID: "valid-backend-tenant-scoped-entity",
			Description: "User proposes a TenantScoped entity with composite key and @SQLDelete preserving tenantId.",
			SeedRequest: "Add a TenantScoped entity InvoiceDataModel with composite key (tenantId, invoiceId) and @SQLDelete with tenantId in the WHERE clause.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"AP3", "PC15"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "valid-webapp-rtk-query-injectendpoints",
			Description: "User adds a new RTK Query slice via baseApi.injectEndpoints per WA-W03.",
			SeedRequest: "Add a tasksApi slice using baseApi.injectEndpoints with getTasks query and createTask mutation, with proper invalidation tags.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"WA-W03"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "valid-mobile-securestore-non-auth",
			Description: "User uses Expo SecureStore for non-auth user preferences (correct primitive).",
			SeedRequest: "Use Expo SecureStore to persist the user's theme preference (light/dark/auto) so it survives reinstall.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"AMS-02"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "valid-e2e-postman-per-request-assertions",
			Description: "User adds per-request assertions (status + content-type + response time) to a Postman collection.",
			SeedRequest: "Add per-request tests to billing-crud-e2e.postman_collection.json: status 200, Content-Type application/json, response time under 500ms.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"knowledge-base/standards/TESTING-STANDARD-BACKEND.md"},
			ScopeHint: []string{"e2e"},
		},
		{
			ID: "valid-infra-tf-module-structure",
			Description: "User scaffolds a Terraform module with the canonical main.tf/variables.tf/outputs.tf split.",
			SeedRequest: "Create a new Terraform module infra/terraform/modules/redis with main.tf, variables.tf (all vars documented), and outputs.tf.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"infra/terraform/modules/"},
			ScopeHint: []string{"infra"},
		},
		{
			ID: "valid-backend-domain-object-stateless",
			Description: "User adds a new Domain object following stateless Path C (DataModel-as-method-param, return-this).",
			SeedRequest: "Add DomainInvoice as a stateless class with methods like markPaid(InvoiceDataModel) returning this; preserve @Component @Scope(prototype).",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"D115", "AP86", "IMF1"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "valid-webapp-zod-schema-boundary",
			Description: "User adds a Zod schema at a form-input boundary on the webapp.",
			SeedRequest: "Add a Zod schema for the TaskCreateForm input that validates title (min 3) and description (max 500) before calling the createTask mutation.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"knowledge-base/patterns/frontend.md"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "valid-mobile-platform-os-guard",
			Description: "User adds a Platform.OS guard around an iOS-only feature in a mobile screen.",
			SeedRequest: "Wrap the FaceID enroll button in a Platform.OS==='ios' guard; show fallback PIN entry on Android.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"knowledge-base/standards/CODE-QUALITY-STANDARD.md"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "valid-webapp-react-lazy-routes",
			Description: "User wraps a route component in React.lazy + Suspense for code-splitting.",
			SeedRequest: "Wrap the AdminOpsPage route in React.lazy with a Suspense fallback skeleton so admin bundle splits out of the main chunk.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"knowledge-base/patterns/frontend.md"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "valid-backend-platform-scoped-singleton",
			Description: "User adds a PlatformScoped (singleton, no tenant column) configuration bean.",
			SeedRequest: "Add a PlatformScoped configuration class FeatureFlagConfiguration as a singleton (no @Scope(prototype), no tenantId column) for cross-tenant feature flags.",
			ExpectedVerdict: VerdictValid,
			TargetCites: []string{"PC15"},
			ScopeHint: []string{"backend"},
		},

		// ===== VALID_WITH_CAVEATS (10) =====
		{
			ID: "vwc-backend-tenant-entity-soft-delete-unclear",
			Description: "User adds a TenantScoped entity but the request doesn't say whether soft delete + @SQLDelete is needed.",
			SeedRequest: "Add a TenantScoped Subscription entity with composite key (tenantId, subscriptionId).",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"AP3"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "vwc-mobile-screen-no-theme-spec",
			Description: "User adds a new mobile screen without specifying Light/Dark/system theme handling.",
			SeedRequest: "Add a UserSettings screen with email/phone/name editing forms.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/templates/MOBILE-VIEW-TEMPLATE.md"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "vwc-webapp-rtk-endpoint-no-cache-strategy",
			Description: "User adds an RTK Query endpoint without saying whether result should be cached or polled.",
			SeedRequest: "Add a getDashboardStats endpoint to dashboardApi.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"WA-W03"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "vwc-backend-usecase-no-test-plan",
			Description: "User adds a new use case but doesn't mention unit + component tests.",
			SeedRequest: "Add SendInvoiceUseCase that emails the invoice PDF to the customer after invoice creation.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/standards/TESTING-STANDARD-BACKEND.md"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "vwc-infra-resource-no-lifecycle",
			Description: "User adds a Terraform resource without lifecycle.prevent_destroy or create_before_destroy.",
			SeedRequest: "Add an aws_rds_cluster resource for the new analytics database in infra/terraform/main.tf.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"infra/terraform/"},
			ScopeHint: []string{"infra"},
		},
		{
			ID: "vwc-webapp-page-no-rbac",
			Description: "User adds a new admin page without specifying role-based access guard.",
			SeedRequest: "Add an /admin/billing page that shows all tenant invoices.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/standards/SECURITY-QUALITY-STANDARD.md"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "vwc-e2e-postman-no-negative-paths",
			Description: "User adds happy-path requests to a Postman collection but no 4xx negative paths.",
			SeedRequest: "Add 3 happy-path requests to login-flows-e2e.postman_collection.json for OAuth Google/Facebook/passkey.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/standards/TESTING-STANDARD-BACKEND.md"},
			ScopeHint: []string{"e2e"},
		},
		{
			ID: "vwc-backend-class-no-stability-tag",
			Description: "User adds a new Java class without stating its stability tier (public-api/internal-api/module-private).",
			SeedRequest: "Add an InvoiceCalculator class in the billing module with computeTotal(items) and computeTax(jurisdiction).",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/standards/CODE-QUALITY-STANDARD.md"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "vwc-mobile-screen-no-offline-handling",
			Description: "User adds a mobile screen that fetches data without offline/loading/error state handling.",
			SeedRequest: "Add a CourseCatalog screen that calls getCourses and renders a list.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/templates/MOBILE-VIEW-TEMPLATE.md"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "vwc-process-decision-no-alternatives",
			Description: "User proposes a new decision-log entry without an Alternatives Considered section.",
			SeedRequest: "Add a new decision D142 to log.yaml: switch from MariaDB to PostgreSQL for the analytics database.",
			ExpectedVerdict: VerdictValidWithCaveats,
			TargetCites: []string{"knowledge-base/decisions/log.yaml"},
			ScopeHint: []string{"platform-process"},
		},

		// ===== INVALID (15) =====
		{
			ID: "invalid-mobile-auth-asyncstorage",
			Description: "Classic AMS-02 violation: persisting Firebase auth material in AsyncStorage.",
			SeedRequest: "Use AsyncStorage to persist Firebase ID tokens so users stay logged in across app restarts.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AMS-02"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "invalid-backend-missing-composite-key",
			Description: "AP3 violation: TenantScoped entity with simple Long id (no composite tenantId+id key).",
			SeedRequest: "Add an entity TenantInvoice with a simple Long id field (no tenantId in the primary key).",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP3"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-backend-mutable-domain-object",
			Description: "AP86 violation: Domain object with mutable instance fields (thread safety bug).",
			SeedRequest: "Add a DomainInvoice class with private fields amount and status that are mutated by markPaid().",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP86", "IMF1"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-webapp-createapi",
			Description: "WA-W03 violation: using RTK Query createApi instead of baseApi.injectEndpoints.",
			SeedRequest: "Create a new tasksApi slice with createApi from @reduxjs/toolkit/query/react for the tasks feature.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"WA-W03"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "invalid-backend-findbyid-bypassing-restriction",
			Description: "AP95 violation: using findById without checking @SQLRestriction tenant filter is active.",
			SeedRequest: "Use repository.findById(invoiceId) to fetch a tenant invoice without checking the Hibernate filter is enabled.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP95"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-backend-hardcoded-secrets",
			Description: "ABS-XX (security) violation: hardcoded secret in Java source.",
			SeedRequest: "Hardcode the JWT signing secret as a static final String in JwtUtil.java for the dev environment.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"knowledge-base/standards/SECURITY-QUALITY-STANDARD.md"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-backend-n-plus-1-stream",
			Description: "AP1 violation: N+1 query loop inside a stream over parent entities.",
			SeedRequest: "Stream tenants and call invoiceRepository.findByTenantId(t.getId()) for each in a parallelStream.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP1"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-backend-multi-statement-no-transactional",
			Description: "Persistence violation: multi-repository use case without @Transactional.",
			SeedRequest: "In SendInvoiceUseCase.execute, call invoiceRepo.save then customerRepo.updateLastInvoiceDate then emailService.send — no @Transactional anywhere.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"knowledge-base/standards/CODE-QUALITY-STANDARD.md"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-webapp-feature-isolation-leak",
			Description: "WA-F (feature isolation) violation: feature A imports a component from feature B directly.",
			SeedRequest: "In features/billing/pages/InvoicesPage.tsx, import TaskRow from features/tasks/components/TaskRow.tsx and render it.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"WA-F01"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "invalid-mobile-biometrics-no-fallback",
			Description: "AMS-01: biometrics gated without PIN/passcode fallback (locks user out if no biometric).",
			SeedRequest: "Force biometric unlock on every app open with no fallback PIN entry.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AMS-01"},
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "invalid-backend-sql-string-concat",
			Description: "AP8 / security: SQL injection via String concatenation in a native query.",
			SeedRequest: "Use em.createNativeQuery(\"SELECT * FROM users WHERE email = '\" + email + \"'\") to look up a user.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP8"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-backend-public-api-no-version",
			Description: "AP6 / api-contract: new public REST endpoint without /v1/ versioning.",
			SeedRequest: "Add a public REST endpoint POST /api/invoices that returns the new invoice id.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP6"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-webapp-localstorage-jwt",
			Description: "AWS-01 / webapp security: storing JWT in localStorage (XSS-accessible).",
			SeedRequest: "Persist the JWT in localStorage.setItem('jwt', token) so the user stays logged in across tabs.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AWS-01"},
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "invalid-backend-skip-archunit",
			Description: "AP11: proposal to FreezingArchRule a violation instead of fixing it.",
			SeedRequest: "Wrap the TenantSoftDeleteGuardFilter ArchUnit test in FreezingArchRule.freeze() so the failing rule passes.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP11"},
			ScopeHint: []string{"backend"},
		},
		{
			ID: "invalid-infra-latest-docker-tag",
			Description: "AP30 / infra: using :latest Docker tag in a Terraform/ECS task definition.",
			SeedRequest: "Set the container image to elatusdev/core-api:latest in the ECS task definition.",
			ExpectedVerdict: VerdictInvalid,
			TargetCites: []string{"AP30"},
			ScopeHint: []string{"infra"},
		},

		// ===== NEEDS_CLARIFICATION (10) =====
		{
			ID: "nc-profile-edit-stack-unspecified",
			Description: "User asks for 'profile edit screen' without specifying webapp or mobile.",
			SeedRequest: "Add a screen for users to edit their profile information.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"webapp", "mobile"},
		},
		{
			ID: "nc-persist-prefs-sensitivity-unspecified",
			Description: "User asks to 'persist user preferences' without saying if sensitive.",
			SeedRequest: "Persist the user's preferences across sessions.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"mobile", "webapp"},
		},
		{
			ID: "nc-api-endpoint-visibility-unspecified",
			Description: "User asks for 'an API endpoint' without saying public vs internal.",
			SeedRequest: "Add an API endpoint to expose the new invoices feature.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"backend"},
		},
		{
			ID: "nc-improve-performance-no-baseline",
			Description: "User asks to 'improve performance' without naming the metric or baseline.",
			SeedRequest: "Improve the performance of the dashboard.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"webapp", "backend"},
		},
		{
			ID: "nc-add-auth-mechanism-unspecified",
			Description: "User asks for 'auth' without specifying mechanism (JWT, OAuth, passkey, etc.).",
			SeedRequest: "Add authentication to the new admin tools.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"backend", "webapp"},
		},
		{
			ID: "nc-add-test-usecase-unspecified",
			Description: "User asks for 'a test' without naming the use case or tier.",
			SeedRequest: "Add a test for the billing module.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"backend"},
		},
		{
			ID: "nc-refactor-shape-unspecified",
			Description: "User asks to 'refactor' a class without saying to what shape.",
			SeedRequest: "Refactor the InvoiceCalculator class.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"backend"},
		},
		{
			ID: "nc-update-deps-strategy-unspecified",
			Description: "User asks to 'update dependencies' without saying major/minor/patch or specific package.",
			SeedRequest: "Update the dependencies for the akademia-plus-web project.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"webapp"},
		},
		{
			ID: "nc-add-caching-tier-unspecified",
			Description: "User asks for 'caching' without saying Redis/CDN/in-memory/browser.",
			SeedRequest: "Add caching to the courses list endpoint.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"backend", "webapp"},
		},
		{
			ID: "nc-add-backup-target-unspecified",
			Description: "User asks for 'backup' without saying DB, S3, repo, or schedule.",
			SeedRequest: "Set up backups.",
			ExpectedVerdict: VerdictNeedsClarification,
			TargetCites: nil,
			ScopeHint: []string{"infra"},
		},

		// ===== OUT_OF_SCOPE (5) =====
		{
			ID: "oos-python-web-scraping",
			Description: "User asks about Python web scraping — outside the platform corpus.",
			SeedRequest: "What's the best Python library for web scraping?",
			ExpectedVerdict: VerdictOutOfScope,
			TargetCites: nil,
			ScopeHint: nil,
		},
		{
			ID: "oos-kubernetes-operator-design",
			Description: "User asks about Kubernetes operators — platform uses ECS, not k8s in prod.",
			SeedRequest: "Design a custom Kubernetes operator to manage tenant lifecycle in production.",
			ExpectedVerdict: VerdictOutOfScope,
			TargetCites: nil,
			ScopeHint: []string{"infra"},
		},
		{
			ID: "oos-ios-swiftui-native",
			Description: "User asks about iOS SwiftUI native — platform uses Expo RN, not native.",
			SeedRequest: "Implement the settings screen in iOS SwiftUI for native performance.",
			ExpectedVerdict: VerdictOutOfScope,
			TargetCites: nil,
			ScopeHint: []string{"mobile"},
		},
		{
			ID: "oos-soap-api-design",
			Description: "User asks about SOAP API design — platform uses REST + Postman.",
			SeedRequest: "Design a SOAP envelope schema for the invoice service.",
			ExpectedVerdict: VerdictOutOfScope,
			TargetCites: nil,
			ScopeHint: []string{"backend"},
		},
		{
			ID: "oos-startup-lang-recommendation",
			Description: "User asks for general programming-language recommendation — corpus is platform-specific.",
			SeedRequest: "What's the best general-purpose programming language to start a SaaS in 2026?",
			ExpectedVerdict: VerdictOutOfScope,
			TargetCites: nil,
			ScopeHint: nil,
		},
	}
}
