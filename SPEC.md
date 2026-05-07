# Memory CLI — Specification

## Vision

A CLI tool that provides unified memory management for multiple AI agents. Agents read, write, search, and consolidate memories through a simple command-line interface. A background daemon continuously processes and refines stored memories.

## Storage

- Root: `~/.memory/`
- Short-term: `~/.memory/short-term/` — TTL-based, auto-expires
- Long-term: `~/.memory/long-term/` — persistent, consolidated
- Index: `~/.memory/index.db` (SQLite) — fast query, tags, search
- Config: `~/.memory/config.yaml`

## Memory Schema

Each memory entry:

```json
{
  "id": "uuid",
  "content": "user prefers dark mode",
  "type": "short" | "long",
  "scope": "global" | "agent:claude" | "agent:copilot",
  "tags": ["preference", "ui"],
  "source": "claude" | "copilot" | "fingersaver" | "logseq" | "obsidian" | "manual",
  "created_at": "2026-05-07T10:00:00Z",
  "updated_at": "2026-05-07T10:00:00Z",
  "expires_at": "2026-05-08T10:00:00Z" | null,
  "access_count": 3,
  "links": ["uuid-of-related-memory"],
  "version": 1
}
```

## CLI Commands

### Core CRUD

```
memory write <content> [--type short|long] [--tags t1,t2] [--scope global|agent:xxx]
memory read <id>
memory delete <id>
memory list [--type short|long] [--scope ...] [--source ...] [--limit N] [--offset N]
memory tag <id> --add t1,t2 --remove t3,t4
```

### Search

```
memory search <query> [--tags ...] [--scope ...] [--type ...] [--from YYYY-MM-DD] [--to YYYY-MM-DD]
```

Full-text search via SQLite FTS5. Supports keyword and tag filtering.

### Ingestion

```
memory ingest --source claude|car-agent|fingersaver|logseq|obsidian|all [--path <custom-path>]
```

Sources:
- **claude**: `~/.claude/` — CLAUDE.md files, project memories, settings
- **car-agent**: `~/.car-agent/` — agent config and memory files
- **fingersaver**: `~/.fingersaver/` — chat history, session context
- **logseq**: Markdown pages, journals, properties, `[[links]]`
- **obsidian**: Markdown notes, frontmatter, tags, aliases

### Lifecycle

```
memory upgrade <id>           # short-term → long-term
memory decay                  # decay unused long-term memories
memory consolidate            # manual trigger: dedup, compress, link
```

### Daemon

```
memory serve [--interval 60s]  # background processing daemon
```

Daemon tasks (runs on interval):
1. **Expire** — remove short-term memories past TTL
2. **Decay** — reduce priority of unused long-term memories
3. **Consolidate** — deduplicate similar memories, merge content
4. **Compress** — summarize verbose memories
5. **Link** — discover relationships between memories
6. **Upgrade** — promote frequently-accessed short-term to long-term

### Export / Import

```
memory export [--output file] [--scope ...] [--type ...]
memory import [--input file]
```

JSONL format, one memory object per line.

## Scope & Isolation

| Scope | Visibility | Example |
|-------|-----------|---------|
| `global` | All agents | User preferences, project conventions |
| `agent:claude` | Claude Code only | Claude-specific context |
| `agent:copilot` | Copilot only | Copilot session state |
| `session:<id>` | Current session only | Ephemeral context |

## Ingestion Adapters

Each source has an adapter that:
1. Discovers files in the source directory
2. Parses format-specific features (frontmatter, links, properties)
3. Normalizes to memory schema
4. Deduplicates against existing memories
5. Writes with `source` tag and appropriate scope

### Logseq Adapter
- Parse `YYYY_MM_DD.md` journal files
- Extract page properties (`key:: value`)
- Convert `[[wikilinks]]` to memory links
- Tag with `logseq` + page tags

### Obsidian Adapter
- Parse markdown frontmatter (YAML)
- Extract tags (`#tag`) and aliases
- Support nested folder structure as hierarchy tags
- Tag with `obsidian` + frontmatter tags

## Concurrency

- SQLite WAL mode for concurrent reads
- File-level locking for writes
- Advisory lock (`~/.memory/.lock`) for daemon — only one daemon instance

## Configuration

`~/.memory/config.yaml`:

```yaml
storage:
  root: ~/.memory
  short_term_ttl: 24h
  decay_threshold: 30d

daemon:
  interval: 60s
  enabled_tasks:
    - expire
    - decay
    - consolidate
    - compress
    - link
    - upgrade

ingestion:
  logseq:
    path: ~/logseq
    enabled: true
  obsidian:
    path: ~/my-vault
    enabled: true

sources:
  claude:
    path: ~/.claude
    enabled: true
  car-agent:
    path: ~/.car-agent
    enabled: true
  fingersaver:
    path: ~/.fingersaver
    enabled: true
```

## Tech Stack

- Go 1.26+
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- Cobra for CLI
- YAML config via `gopkg.in/yaml.v3`
- FTS5 for full-text search

## Project Structure

```
memory-cli/
├── main.go
├── internal/
│   ├── store/          # SQLite storage, CRUD, FTS
│   │   ├── store.go
│   │   ├── memory.go
│   │   └── search.go
│   ├── ingest/         # Ingestion adapters
│   │   ├── ingest.go
│   │   ├── claude.go
│   │   ├── car_agent.go
│   │   ├── fingersaver.go
│   │   ├── logseq.go
│   │   └── obsidian.go
│   ├── daemon/         # Background processing
│   │   ├── daemon.go
│   │   ├── expire.go
│   │   ├── decay.go
│   │   ├── consolidate.go
│   │   ├── compress.go
│   │   ├── link.go
│   │   └── upgrade.go
│   ├── config/         # Config loading
│   │   └── config.go
│   └── cmd/            # Cobra commands
│       ├── root.go
│       ├── write.go
│       ├── read.go
│       ├── delete.go
│       ├── list.go
│       ├── search.go
│       ├── tag.go
│       ├── ingest.go
│       ├── consolidate.go
│       ├── upgrade.go
│       ├── decay.go
│       ├── serve.go
│       ├── export.go
│       └── import.go
├── go.mod
├── go.sum
├── SPEC.md
└── CLAUDE.md
```

## Implementation Order

### Phase 1: Core (read/write/store)
- SQLite store with memory schema
- `write`, `read`, `delete`, `list` commands
- Config loading
- Basic test coverage

### Phase 2: Search & Lifecycle
- FTS5 full-text search
- `search` command
- `tag` command
- `upgrade` / `decay` commands

### Phase 3: Ingestion
- Adapter interface
- Claude adapter
- Fingersaver adapter
- Logseq adapter
- Obsidian adapter
- Car-agent adapter

### Phase 4: Daemon
- Background daemon with task runner
- Expire, decay, consolidate, compress, link, upgrade tasks
- Advisory lock

### Phase 5: Export/Import
- JSONL export/import
- Scope filtering
