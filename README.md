# Memory CLI

Unified memory management for multiple AI agents.

Agents (Claude Code, Copilot, FingerSaver, etc.) read, write, search, and consolidate memories through CLI, JSON-RPC over stdin, or persistent Unix socket. A background daemon continuously classifies, links, and refines stored memories.

## Install

```bash
go build -o memory .
# or
go install github.com/cybernagle/memory-cli@latest
```

## Quick Start

```bash
# Initialize (creates ~/.memory/)
memory write "user prefers dark mode" --category preferences --tags ui

# Read back
memory read <id>

# Search
memory search "dark mode"

# List by category
memory list --category knowledge

# Ingest from existing sources
memory ingest --source claude

# Start daemon + socket server
memory serve
```

## Storage Model

Memories progress through phases across 15 categories:

```
inbox → processed → organized   (Extract+Merge 加工链)
```

Memories are stored in **SQLite** (`~/.memory/memories.db`), not flat files. Each memory carries:

- `phase` — inbox / processed / organized
- `category` — knowledge / preferences / decisions / project / people / evidence / … (15 total)
- `metadata` (JSON) — version tracking (`superseded_by`), proposals status, evidence aggregates, provenance (tmux_session, prompt_id, …)
- `source` — provenance: who/what wrote it (e.g. `claude`, `makro-brain`, `consolidate`, `evidence-task`)
- `tags`, `links` (bidirectional `[[wikilinks]]`)

Provenance columns (`tmux_session`, `prompt_id`, user+assistant turns) enable full traceability from any organized memory back to its source conversation.

> **See [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) for the full layered architecture** — 5 layers (foundation → core → domain → orchestration → composition), data-flow diagrams, and the cross-layer rules every feature change should follow.

## CLI Reference

### Core CRUD

```bash
memory write <content> [--category <cat>] [--tags t1,t2] [--scope global] [--source <name>]
memory read <id>
memory delete <id>
memory list [--phase inbox|organized] [--category <cat>] [--scope ...] [--source ...]
```

### Search

```bash
memory search <query> [--tags ...] [--category ...] [--from YYYY-MM-DD] [--to YYYY-MM-DD]
```

### Bidirectional Links

Memories can reference each other with `[[wikilink]]` syntax. Wikilinks are resolved automatically during ingest, and the `EntityExtractionTask` (daemon) keeps the entity graph + backlinks up to date — no manual link commands needed.

### Processing & Consolidation

The daemon runs an Extract+Merge pipeline (via `factprocessor`, the canonical implementation through the `plugin` contract) plus several background refinement tasks:

```bash
memory serve            # start daemon: consolidate / enrich / profile / entity / evidence / reminder
```

**Daemon tasks** (run on `memory serve`):
- `ConsolidateLLMTask` — processed → organized (LLM merge)
- `EnrichTagsTask` — tag enrichment
- `ProfileTask` — user profile synthesis (→ `character`)
- `EntityExtractionTask` — LLM entity discovery (fills the entity graph)
- `EvidenceTask` — proposal accept/reject aggregation (→ `evidence`)
- `ReminderTask` — due reminders → macOS notifications

### Notifications

```bash
memory notify
```

Checks due reminders and pushes macOS notifications. Writes pending reminders to `~/.memory/pending.md` for agents to consume on startup.

### Daemon

```bash
memory serve [--interval 60s]
```

Starts background daemon (consolidate-llm / enrich / profile / entity-extract / evidence / reminder) plus Unix socket server at `~/.memory/memory.sock`. This is the single entry point for all background processing.

### Ingestion

```bash
memory ingest --source claude|car-agent|fingersaver|logseq|obsidian|all
```

### Export / Import

```bash
memory export [--output file] [--category ...]
memory import [--input file]
```

JSONL format, one memory per line.

## Agent Integration

### Method 1: CLI

Any agent that can run shell commands:

```bash
memory write "user prefers vim" --category preferences
memory search "editor preference"
```

### Method 2: MCP (stdio JSON-RPC) — for Claude Code / zcode / makro

```bash
# List tools
memory mcp
```

Speak JSON-RPC 2.0 over stdio (initialize → tools/call). 6 tools: `memory_ask`, `memory_search`, `memory_write`, `memory_read`, `memory_list`, `memory_search`.

### Method 3: Unix Socket — persistent connection (daemon running)

```bash
# Connect
socat - UNIX-CONNECT:~/.memory/memory.sock

# List tools
{"id":1,"method":"tools/list","params":{}}

# Search
{"id":2,"method":"tools/call","params":{"name":"memory_search","params":{"query":"dark mode"}}}

# Shorthand: use tool name directly as method
{"id":3,"method":"memory_list","params":{"category":"knowledge"}}
```

Each request/response is a single JSON line.

## Configuration

`~/.memory/config.yaml`:

```yaml
storage:
  root: ~/.memory
  short_term_ttl: 168h    # Inbox TTL (7 days)

daemon:
  interval: 60s
  decay_threshold: 720h   # 30 days
  upgrade_access_threshold: 3

notification:
  enabled: true
  method: osascript

timezone: "Asia/Shanghai"
```

## Architecture

memory-cli is a **5-layer** system with strict bottom-up dependencies (the dependency graph is a clean DAG — no cycles):

```
⑤ cmd            — composition root (cobra wiring, 18 subcommands)
④ daemon/api/mcp/transport — orchestration (scheduling + protocol adapters)
③ ingest/factprocessor/entity/plugin/query/dashboard/agent — domain (semantic processing)
② store          — core storage (SQLite, 12 dependents) ★
① config/llm/notify/health — foundation (zero internal deps)
```

**Full diagram + layer responsibilities + cross-layer rules**: see [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md).

## Development

```bash
go build -o memory .
go test ./...
go vet ./...
go fmt ./...
```

## License

MIT
