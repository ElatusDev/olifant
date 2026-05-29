# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Olifant is a Go CLI that acts as a local model orchestrator for the
ElatusDev/AkademiaPlus platform. It builds a corpus from the platform
knowledge base, embeds + retrieves over it (RAG), synthesizes
execution plans, runs those plans across local + cloud models, and
validates the results. It is a sibling of `platform-knowledge-base/`
inside the `platform/` tree and is meant to be run from there.

**Path gotcha:** specs and code comments say `platform/knowledge-base/`,
but the directory on disk is `platform-knowledge-base/`. The corpus
builder autodetects KB by walking up for `knowledge-base/README.md`,
which will NOT match this layout — pass `--kb-root
../platform-knowledge-base` explicitly to `corpus`/`repo` commands.

## Commands

```
make build        # go build -o bin/olifant .
make tidy         # go mod tidy
make test         # go test ./...
make fmt          # go fmt ./...
make vet          # go vet ./...
make corpus       # build + run `olifant corpus build -v`
```

Requires Go 1.26+. The Makefile pins `GO ?= /opt/homebrew/bin/go`;
override `GO=` if your toolchain lives elsewhere.

Run a single package's tests: `go test ./internal/psp/...`
Run one test: `go test ./internal/psp/ -run TestName -v`
Match CI locally: `go test ./... -race -count=1`

### CI gates (.github/workflows/ci.yml)

All must pass on PRs to `main`:
- `go vet ./...`
- `golangci-lint` (v2.12.2, config in `.golangci.yml`)
- `go test ./... -race -count=1`
- **Per-package coverage ≥ 80%** — a hard gate with no exemptions.
  EVERY package (including the root) must hit the floor; a package
  with no `_test.go` files scores 0% and fails the build. Raise
  coverage by writing tests, **never** by adding coverage exclusions.
- `govulncheck ./...`

## CLI structure

`main.go` is pure subcommand dispatch; each subcommand is a function in
`cmd/<name>.go`. The pipeline subcommands, in rough data-flow order:

- `corpus build` — walk `knowledge-base/`, per-repo `CLAUDE.md`, and
  `memory/`; emit per-scope NDJSON + `manifest.yaml` under
  `knowledge-base/corpus/v1/`.
- `repo ingest` — chunk + embed source from the 7 platform repos.
- `history scan` — walk repo commit history → JSONL training data.
- `dataset build|index` — extract Tier 1+2 training JSONL (patterns,
  decisions, anti-patterns, failure-modes); index failure-modes to ChromaDB.
- `prompt build "<goal>"` — embed → retrieve → synth a PSP v1 plan,
  written to `plans/<plan_id>.yaml`. Auto-splits when steps exceed
  `psp.MaxStepsPerPlan` (25).
- `plan validate|split` — manage PSP plan files.
- `run --plan <file>` — execute a plan via the PSP runner.
- `eval run --suite <file>` — run an eval suite battery.
- `validate --claim <ref> --diff <ref>` — post-Claude claim-vs-evidence audit.
- `challenge "<request>"` — step 0, produce a verdict in YAML.
- `turn list|show|stats` — inspect the short-term event ledger.
- `embedder train|pull|ls` — domain-embedder training on Modal.

## Architecture

**Scopes.** Everything is organized around 7 scopes (`internal/corpus/scope.go`):
`universal, backend, webapp, mobile, e2e, infra, platform-process`.
Path → scope mapping is prefix-based and order-sensitive (first match
wins, most specific first). Corpus NDJSON, repo chunks, and retrieval
filters are all scope-partitioned. When you add a knowledge-base path
or repo, update `kbScopeRules` / `ScopeForRepoClaudeMd`.

**Runtime config (`internal/config/config.go`).** `config.Resolve()` is
the single source of endpoints + model names, all overridable via
`OLIFANT_*` env vars so the same binary runs from a laptop, the Olifant
Mac mini, or a k8s pod over the tailnet. Defaults: Ollama at
`100.94.233.106:11434`, ChromaDB at `localhost:8000` (assumes a
port-forward), embedder `nomic-embed-text`, synthesizer
`qwen2.5:14b-instruct-q6_K`. Subcommands should populate their Config
structs from `config.Resolve()` rather than reading env directly.

**PSP — Prompt-Step Protocol v1 (`internal/psp/`).** The execution
engine, modeled as a TCP-like state machine (SYN/ACK/STEP/FIN segments,
states in `types.go`). The runner walks a `Plan`'s steps, dispatches
each to an `Executor`, validates per-step output against the step's
JSON schema, retries on NAK, and records every transition to the
short-term ledger, emitting an `Aggregate` to
`short-term/plans/<plan_id>/aggregate.yaml`.

**Hybrid executors.** Each step routes to a named executor via
`RunnerConfig.Executors[step.ResolvedExecutor()]`. Two kinds:
`local` (Ollama, the default — empty `executor:` field means local for
backward compat) and `claude` (the `claude` CLI as a subprocess,
`internal/psp/claude_code_executor.go`, billed against the Claude
subscription, no API key). Claude-routed steps abort at pre-flight if
the `claude` binary is missing. Config via `OLIFANT_CLAUDE_*` env vars.

**RAG path (`internal/prompt/`, `internal/chroma/`, `internal/ollama/`).**
`prompt build` embeds the goal (Ollama), retrieves top-N scope-filtered
chunks (Chroma), and synthesizes a grammar-constrained JSON plan
(Ollama synthesizer). Note: step-id format (`step_NN`) is enforced
post-parse in Go, not in the synth schema — nested schema constraints
crashed Ollama's grammar engine.

**Embedder training (`internal/embedder/`, `cmd/embedder.go`).** Phase
B1b domain-embedder training runs on Modal; `modal_app.py` is the
remote app, the Go side drives `train|pull|ls`.

## Conventions

- Linting carve-outs in `.golangci.yml` are a documented pre-existing
  baseline (as of 2026-05-15), not a license to add more. New code
  should pass the standard linter set clean.
- Plans authored before a feature existed must keep working — note the
  backward-compat handling already present (empty `executor:`, zero-value
  cache fields in `StepResult`/`StepSummary`) and preserve it.
- Specs that govern on-disk formats live in the sibling KB (see below).
  Consult them before changing any shape Olifant reads or writes.

## Platform knowledge base (`../platform-knowledge-base/`)

This sibling repo is the source of truth for everything non-code and is
both the corpus source and the spec authority for Olifant's data shapes.
It is a layered model: Layer 1 markdown/YAML source → Layer 2 frozen CNL
vocabulary → Layer 3 growing dictionaries → Layer 4 derived corpus NDJSON
→ Layer 5 ChromaDB vector index → Layer 6 Olifant. The governing specs:

- `architecture/kb-overview.md` — the layered model + filesystem layout.
- `corpus/CORPUS-V1.md` — corpus chunk schema, chunking rules, the 7-scope
  source map, build determinism, rebuild triggers.
- `dsl/cnl-v1.md` — Controlled Natural Language: the closed-vocabulary
  language for prompts/verdicts/validator output, EBNF grammar, dictionary
  entry schema.
- `dsl/psp-v1.md` — the full Prompt-Step Protocol spec (TCP mapping, state
  machine, every segment shape, MPS=25 cap, splitting/retry semantics).

**Corpus model.** Source files are canonical; the corpus is *derived* and
rebuilt deterministically (idempotent → byte-identical NDJSON on unchanged
input). Chunk IDs are content-derived SHA1s; `source_sha` comes from
`git ls-files`, so only changed sources re-embed. `v1/manifest.yaml` is
committed (so SHA/count deltas show in review); `v1/*.ndjson` is gitignored.
Chunking is structure-aware: one chunk per top-level YAML entry for
standards/decisions/anti-patterns; section-aware (`##`) for markdown.
Citations (`D\d+`, `AP\d+`, `S[BIMXW]-\d+`, …) are regex-extracted from
body text. Repo *source code* is excluded from v1 (too large) — only KB,
per-repo `CLAUDE.md`, and `memory/` are corpus sources.

**CNL + dictionaries.** `dictionary/<scope>/` holds the vocabulary.
`symbols.yaml`, `connectors.yaml`, `modifiers.yaml` are **frozen** in v1
(universal scope only; changing them needs a CNL minor-version bump).
`domain.yaml`/`subjects.yaml`/`actions.yaml` grow append-only, and the
*only* legal path to add a term is the `challenge` step (gated by user
accept). Word resolution: tech-stack scope first, then universal fallback.

**PSP runtime.** The protocol the `internal/psp/` runner implements. v0/v1
shipping reality: window=1 (strict sequential, no parallel steps),
MPS hard-enforced at plan-load, splitting + retry/backoff live, ledger
turn written on every state transition. Segments are YAML, in-process (no
network framing). When changing runner behavior, the spec is authoritative —
keep the state machine and segment shapes aligned with `psp-v1.md`.
