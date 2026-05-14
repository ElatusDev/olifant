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
