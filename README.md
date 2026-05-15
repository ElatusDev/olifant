# olifant

Local model orchestrator for the ElatusDev/AkademiaPlus platform —
prompt-compaction, challenge, validation, and retrieval over the platform
knowledge base.

Spec: [`platform/knowledge-base/corpus/CORPUS-V1.md`](../knowledge-base/corpus/CORPUS-V1.md)
Language: [`platform/knowledge-base/dsl/cnl-v1.md`](../knowledge-base/dsl/cnl-v1.md)

## Build

```
make tidy build
./bin/olifant version
```

Requires Go 1.26+.

## Subcommands

### `olifant corpus build`

Walks `platform/knowledge-base/`, per-repo `CLAUDE.md` files, and
`memory/`, emits per-scope NDJSON + manifest.yaml under
`knowledge-base/corpus/v1/`.

```
./bin/olifant corpus build -v
```

Flags:
- `--kb-root` (default: autodetected by walking up from cwd)
- `--platform-root` (default: parent of `--kb-root`)
- `--memory-root` (default: `$HOME/.claude/projects/.../memory` or `<platform>/memory`)
- `--out` (default: `<kb-root>/corpus/v1`)
- `-v` verbose progress

### `olifant corpus diff` — not yet implemented
### `olifant corpus index` — not yet implemented (waits on ChromaDB)

### `olifant prompt build "<goal>"`

Decomposes a high-level goal into a PSP v1 plan via embed → retrieve → synth.
Walks corpus + code + history + code_history collections (all scopes by
default), produces a JSON plan via grammar-constrained decoding, and writes
`plans/<plan_id>.yaml`. Auto-splits via `psp.Split()` when the synthesizer
emits more than `psp.MaxStepsPerPlan` (25) steps.

```
./bin/olifant prompt build "add a /healthz endpoint to elatus-rest-api with multitenancy"
./bin/olifant plan validate plans/<emitted-plan-id>.yaml
```

Flags:
- `--scope` comma-separated scope filter (default: all 7 scopes)
- `--top` chunks to retrieve globally after distance sort (default: 8)
- `--temperature` synthesizer temperature (default: 0.1)
- `--max-tokens` synthesizer num_predict (default: 1024)
- `--timeout` overall timeout in seconds (default: 600)
- `--synth` synthesizer model override
- `--out` output directory (default: `plans`)
- `--no-record` skip short-term turn record
- `-v` verbose retrieval + synth log

Emits one path per line on stdout (multiple for split plans); metrics +
warnings on stderr.

## Hybrid execution (Claude Code subprocess + local Ollama)

The PSP runner supports routing each step to one of two executors:

- `local` — Ollama-hosted synthesizer on the Olifant Mac mini (default for all steps).
- `claude` — `claude` CLI subprocess (Claude Code), authenticated against your
  existing Claude subscription. For steps that benefit from stronger reasoning.

Enable Claude by having the `claude` binary on PATH (already installed if
you're using Claude Code). No API key needed — the CLI handles auth via the
keychain/OAuth tokens from `claude /login`.

Optional env overrides:

```bash
export OLIFANT_CLAUDE_BINARY=/opt/homebrew/bin/claude   # if not on PATH
export OLIFANT_CLAUDE_MODEL=claude-sonnet-4-6           # or opus-4-7, haiku-4-5
export OLIFANT_CLAUDE_EFFORT=high                       # low|medium|high|xhigh|max
export OLIFANT_CLAUDE_TIMEOUT=120                       # seconds per step
```

Per-step routing lives in the plan YAML:

```yaml
steps:
  - id: lookup_step
    description: Resolve cite paths against the dictionary.
    executor: local                # cheap lookup → Ollama
    expected_output: { schema: { type: object } }

  - id: reasoning_step
    description: Audit code change against the standards catalogue.
    executor: claude               # multi-step reasoning → Claude
    expected_output: { schema: { type: object } }

  - id: default_step
    description: Implicit local executor — no field needed.
    expected_output: { schema: { type: object } }
```

Plans authored before this change keep working: an unset `executor:` field is
treated as `local`. Plans that reference `claude` without the `claude` binary
available abort at pre-flight with a clear error — they never start executing.

Cost: Claude Code calls are billed against your Claude subscription
(no per-call API charges). Claude Code performs its own prompt caching
internally; the cache stats are surfaced on each step's StepResult via
`cache_creation_tokens` / `cache_read_tokens` (read from
`result.usage.cache_*_input_tokens` in the CLI's JSON output). The
`cache totals` line in `olifant run -v` shows the plan-wide hit rate.

Verbose smoke output (per step):

```
  step 1 classify_request  executor=local   id=qwen2.5:14b-instruct-q6_K  elapsed=12526ms  cache(rw)=0/0
  step 2 synthesize        executor=claude  id=claude-sonnet-4-6          elapsed=2103ms   cache(rw)=3502/4910
  step 3 final_summary     executor=local   id=qwen2.5:14b-instruct-q6_K  elapsed=2892ms   cache(rw)=0/0
  cache totals — read=3502 created=4910 hit_rate=41.6%
```

The cost-design rationale is captured in
`knowledge-base/architecture/olifant-training-plan.md` §Hybrid mode.

## Layout

```
olifant/
├── main.go                  # CLI entry, subcommand dispatch
├── cmd/                     # subcommand wiring (corpus, challenge, query, ...)
├── internal/corpus/         # corpus builder
│   ├── types.go             # Chunk, Manifest, Config
│   ├── scope.go             # path → scope mapping
│   ├── cites.go             # citation regex over body text
│   ├── git.go               # source SHA via git ls-files
│   ├── yaml_chunker.go      # standards/decisions/anti-patterns → 1 chunk per entry
│   ├── md_chunker.go        # markdown → section-aware chunking
│   └── builder.go           # orchestration + NDJSON + manifest writing
└── Makefile
```
