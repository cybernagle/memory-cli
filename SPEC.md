# Memory CLI — Specification

## Vision

A CLI tool that provides unified memory management for multiple AI agents. Agents read, write, search, and consolidate memories through a command-line interface, JSON-RPC over stdin, or persistent Unix socket. A background daemon continuously processes and refines stored memories using a progressive disclosure model (inbox → organized) with category-based classification.

## Storage

- Root: `~/.memory/`
- Categories directory: `~/.memory/categories/` — contains one subdirectory per category
- Inbox: `~/.memory/categories/inbox/` — newly written memories await classification
- Organized categories: `~/.memory/categories/{people,project,knowledge,...}/` — classified memories
- Reminders: `~/.memory/categories/reminders/` — time-based reminders
- Config: `~/.memory/config.yaml`
- Memory index: `~/.memory/memory.md`
- Pending reminders: `~/.memory/pending.md`

Each memory is stored as a Markdown file with YAML frontmatter, using atomic writes (write to `.tmp` then rename) for safety.

### Phase & Category Model

Memories progress through two phases:

| Phase | Meaning | Directory |
|-------|---------|-----------|
| `inbox` | Newly written, awaiting classification | `categories/inbox/` |
| `organized` | Classified into a category | `categories/{category}/` |

Categories (14 total):

| Category | Description |
|----------|-------------|
| `soul` | Core identity, values, mission |
| `character` | Personality traits, communication style |
| `people` | People and relationships |
| `project` | Project context, repos, deployments |
| `date` | Time expressions, deadlines, schedules |
| `knowledge` | Technical knowledge, facts, patterns |
| `feedback` | Feedback, suggestions, improvements |
| `preferences` | User/agent preferences |
| `decisions` | Design decisions, choices made |
| `lessons` | Lessons learned, gotchas |
| `habits` | Behavioral patterns, routines |
| `skills` | Skills, capabilities |
| `inbox` | Unclassified (initial landing) |
| `reminders` | Time-based reminders |

### File Format

```markdown
---
id: 550e8400-e29b-41d4-a716-446655440000
content_hash: a1b2c3...
phase: organized
category: preferences
scope: global
tags:
  - preference
  - ui
source: claude
created_at: 2026-05-07T10:00:00Z
updated_at: 2026-05-07T10:00:00Z
expires_at: null
access_count: 3
links:
  - 7f8d9e00-...
version: 1
---
user prefers dark mode
```

## Memory Schema

```go
type Phase string    // "inbox" | "organized"

type Category string // "soul" | "character" | "people" | "project" | "date" |
                     // "knowledge" | "feedback" | "preferences" | "decisions" |
                     // "lessons" | "habits" | "skills" | "inbox" | "reminders"

type Memory struct {
    ID          string     // UUID
    Content     string     // Memory content
    ContentHash string     // SHA256 for deduplication
    Phase       Phase      // inbox or organized
    Category    Category   // One of 14 categories
    Scope       string     // global, agent:claude, etc.
    Tags        []string
    Source      string     // claude, copilot, logseq, etc.
    CreatedAt   time.Time
    UpdatedAt   time.Time
    ExpiresAt   *time.Time // nil for organized memories
    AccessCount int        // For upgrade decisions
    Links       []string   // Related memory IDs (bidirectional)
    Version     int
}
```

## CLI Commands

### Core CRUD

```
memory write <content> [--category knowledge] [--tags t1,t2] [--scope global|agent:xxx] [--source xxx]
memory read <id>
memory delete <id>
memory list [--phase inbox|organized] [--category ...] [--scope ...] [--source ...] [--limit N]
```

### Search

```
memory search <query> [--tags ...] [--scope ...] [--phase ...] [--category ...] [--from YYYY-MM-DD] [--to YYYY-MM-DD]
```

In-memory keyword matching (`strings.Contains`, case-insensitive) with tag and date-range filtering. The agent framework also provides `memory_smart_search` which combines search with tag filtering and relevance heuristics.

### Bidirectional Links

```
memory link <source-id> <target-id>       # Create bidirectional link
memory unlink <source-id> <target-id>     # Remove bidirectional link
memory resolve-links                      # Scan [[wikilinks]] and resolve backlinks
memory backlinks <id>                     # Show all memories linking to this one
```

Memories can reference each other using `[[wikilink]]` syntax in content. `resolve-links` scans all memories, extracts wikilinks, and updates the `Links` field on target memories. Links are bidirectional — linking A to B also links B to A.

### Dream Engine

```
memory dream [--level 1|2|3]
```

The dream engine processes memories during idle time with three depth levels:

| Level | Name | Actions |
|-------|------|---------|
| 1 | Light | Classify inbox memories by content heuristics into categories |
| 2 | Medium | Light + merge similar content + resolve [[wikilinks]] |
| 3 | Deep | Medium + cross-category analysis + extract reminders from time expressions |

Classification heuristics support both Chinese and English keywords (e.g., "偏好"/"prefers" → `preferences`, "教训"/"learned" → `lessons`).

### Notifications

```
memory notify
```

Checks for due reminders and pushes notifications:
- macOS notifications via `osascript`
- Writes pending reminders to `~/.memory/pending.md` for agents to consume on startup

### Ingestion

```
memory ingest --source claude|car-agent|fingersaver|logseq|obsidian|all [--path <custom-path>]
```

Sources:
- **claude**: `~/.claude/` — CLAUDE.md files, project memories
- **car-agent**: `~/.car-agent/` — all .md files (truncated to 5000 chars)
- **fingersaver**: `~/.fingersaver/` — chat.md (truncated to 5000 chars)
- **logseq**: Markdown pages, journals, `[[wikilinks]]`
- **obsidian**: Markdown notes, YAML frontmatter tags/aliases

Ingestion deduplicates via SHA256 content hash. Ingested memories are written as `inbox` phase.

### Lifecycle

```
memory upgrade <id>           # inbox → organized (moves file to category directory)
memory decay                  # remove unused organized memories (age > threshold, access_count == 0)
memory consolidate            # run all daemon tasks once: expire, decay, upgrade, dedup
```

### Daemon

```
memory serve [--interval 60s]  # background processing daemon + Unix socket server
```

Daemon tasks (runs on interval):
1. **Expire** — remove inbox memories past TTL (default 7 days)
2. **Decay** — remove unused organized memories (age > threshold AND access_count == 0)
3. **Upgrade** — promote inbox memories with access_count >= threshold to organized
4. **Consolidate** — deduplicate by content prefix (first 100 chars), keep highest access_count

The `serve` command also starts a Unix socket server at `~/.memory/memory.sock` for persistent agent connections.

### Export / Import

```
memory export [--output file] [--scope ...] [--category ...]
memory import [--input file]
```

JSONL format, one memory object per line.

## Agent Communication

Agents can interact with memory-cli through three methods:

### Method 1: CLI Direct Invocation

The simplest method — any agent that can execute shell commands can use memory-cli directly:

```bash
# Write a memory
memory write "user prefers dark mode" --category preferences --tags ui

# Search memories
memory search "dark mode"

# List all knowledge memories
memory list --category knowledge

# Use agent framework with prompt
memory agent --prompt "user preferences"
```

**Best for**: One-off commands, scripting, quick queries.

### Method 2: Agent stdin JSON-RPC

The `memory agent` command accepts JSON-RPC calls via stdin, enabling batched tool execution:

```bash
# List available tools
memory agent --list-tools

# Execute tools via stdin
echo '[{"name":"memory_write","params":{"content":"hello","category":"knowledge"}}]' | memory agent

# Multi-step with context injection
echo '{"prompt":"user preferences about UI"}' | memory agent --prompt -
```

**Tool Names**: `memory_write`, `memory_read`, `memory_delete`, `memory_list`, `memory_search`, `memory_tag`, `memory_smart_search`

**Best for**: LLM agents that can output JSON tool calls (Claude Code, Copilot, etc.).

### Method 3: Unix Socket (Persistent Connection)

When running `memory serve`, a Unix socket server listens at `~/.memory/memory.sock`. Agents can connect and send JSON-RPC requests over the persistent connection:

```bash
# Connect to socket
socat - UNIX-CONNECT:~/.memory/memory.sock

# List tools
{"id":1,"method":"tools/list","params":{}}

# Call a tool
{"id":2,"method":"tools/call","params":{"name":"memory_search","params":{"query":"dark mode"}}}

# Direct tool name (shorthand)
{"id":3,"method":"memory_list","params":{"category":"knowledge"}}
```

Each request is a single JSON line, responses are single JSON lines. The connection stays open for multiple requests.

**Best for**: Long-running agents, daemons, persistent integrations.

### Agent Framework

All three methods are backed by the same agent framework:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() ToolSchema
    Execute(ctx context.Context, params map[string]any) (ToolResult, error)
}
```

**7 Memory Tools**: `memory_write`, `memory_read`, `memory_delete`, `memory_list`, `memory_search`, `memory_tag`, `memory_smart_search`

**Hooks**: `BeforeToolCall` (can block), `AfterToolCall` (can modify result), `OnEvent` (side-effect logging)

**Events**: `agent_start`, `agent_end`, `tool_start`, `tool_end`, `error` (broadcast channel)

**Session**: Context injection via `InjectContext(query)` — searches relevant memories and sets context for tool execution.

## Scope & Isolation

| Scope | Visibility | Example |
|-------|-----------|---------|
| `global` | All agents | User preferences, project conventions |
| `agent:claude` | Claude Code only | Claude-specific context |
| `agent:copilot` | Copilot only | Copilot session state |

## Concurrency

- Atomic file writes (`.tmp` + rename)
- Advisory lock (`~/.memory/.daemon.lock` with PID) for daemon — only one instance
- Signal handling (SIGINT/SIGTERM) for graceful shutdown
- Unix socket connection handling with 1MB buffer per connection

## Configuration

`~/.memory/config.yaml`:

```yaml
storage:
  root: ~/.memory
  short_term_ttl: 168h    # inbox TTL: 7 days (supports "30d" for day-based durations)

daemon:
  interval: 60s
  decay_threshold: 720h   # 30 days
  upgrade_access_threshold: 3

notification:
  enabled: true
  method: osascript       # "osascript" | "file"

timezone: "Asia/Shanghai"  # Optional, for reminder time parsing

ingestion:
  logseq:
    path: ~/logseq
    enabled: true
  obsidian:
    path: ~/my-vault
    enabled: true
```

## Tech Stack

- Go 1.26+, no CGO
- Cobra for CLI framework (`github.com/spf13/cobra`)
- YAML frontmatter via `gopkg.in/yaml.v3`
- UUID via `github.com/google/uuid`
- File-based storage with atomic writes

## Project Structure

```
memory-cli/
├── main.go
├── internal/
│   ├── store/          # File-based storage, CRUD, search, links
│   │   ├── store.go    # Core operations: Write, Read, Delete, List, Tag, Upgrade
│   │   ├── memory.go   # Memory data model (Phase, Category)
│   │   ├── search.go   # In-memory search with filtering
│   │   └── link.go     # Bidirectional links, backlinks, wikilink resolution
│   ├── agent/          # Agent framework
│   │   ├── agent.go    # Agent core: registry, execute, subscribe
│   │   ├── tool.go     # Tool interface and types
│   │   ├── event.go    # Event types
│   │   ├── hooks.go    # Hook types
│   │   ├── session.go  # Session with context injection
│   │   ├── register.go # RegisterAll helper
│   │   ├── memory_write.go
│   │   ├── memory_read.go
│   │   ├── memory_delete.go
│   │   ├── memory_list.go
│   │   ├── memory_search.go
│   │   ├── memory_smart_search.go
│   │   ├── memory_tag.go
│   │   └── util.go     # Shared helpers
│   ├── transport/      # Transport layer
│   │   └── socket.go   # Unix socket JSON-RPC server
│   ├── ingest/         # Ingestion adapters
│   │   ├── ingest.go   # Adapter interface
│   │   ├── util.go     # Shared utilities
│   │   ├── claude.go
│   │   ├── car_agent.go
│   │   ├── fingersaver.go
│   │   ├── logseq.go
│   │   └── obsidian.go
│   ├── daemon/         # Background processing
│   │   ├── daemon.go   # Orchestrator, Task interface
│   │   ├── expire.go   # Remove expired inbox memories
│   │   ├── decay.go    # Remove unused organized memories
│   │   ├── upgrade.go  # Promote frequently-accessed to organized
│   │   ├── consolidate.go  # Deduplicate
│   │   ├── dream.go    # Dream engine (3 levels), idle detection
│   │   └── notify.go   # Reminder notifications
│   ├── config/         # Config loading
│   │   └── config.go
│   └── cmd/            # Cobra commands
│       ├── root.go
│       ├── write.go
│       ├── read.go
│       ├── delete.go
│       ├── list.go
│       ├── search.go
│       ├── upgrade.go
│       ├── decay.go
│       ├── consolidate.go
│       ├── link.go         # link, unlink, resolve-links
│       ├── backlinks.go    # backlinks
│       ├── dream.go        # dream --level 1|2|3
│       ├── notify.go       # notify
│       ├── serve.go        # serve (daemon + socket)
│       ├── ingest.go
│       ├── export.go
│       ├── import.go
│       └── agent.go        # Agent mode CLI
├── go.mod
├── go.sum
├── SPEC.md
├── MEMORY.md
└── CLAUDE.md
```

## Implementation Status

### Phase 1: Core (Complete)
- File-based store with YAML frontmatter
- `write`, `read`, `delete`, `list` commands
- Config loading with defaults

### Phase 2: Search & Lifecycle (Complete)
- In-memory keyword search with tag/date filtering
- `upgrade` / `decay` / `consolidate` commands

### Phase 3: Ingestion (Complete)
- Adapter interface with 5 adapters
- Claude, Car-Agent, FingerSaver, Logseq, Obsidian
- Deduplication via content hash

### Phase 4: Daemon (Complete)
- Background daemon with task runner
- Expire, Decay, Upgrade, Consolidate tasks
- Advisory lock with signal handling

### Phase 5: Export/Import (Complete)
- JSONL export/import with scope filtering

### Phase 6: Agent Framework (Complete)
- Tool interface with 7 memory tools
- Agent with registry, hooks (before/after), event broadcasting
- Session with context injection
- `memory agent` CLI command with JSON stdin

### Phase 7: Progressive Disclosure (Complete)
- Phase (inbox/organized) + Category (14 types) replacing short-term/long-term
- Bidirectional wikilinks with `[[link]]` syntax
- Dream engine with 3 levels (light/medium/deep)
- Idle detection for automatic dream level selection
- Notification system with macOS push + pending.md
- Unix socket JSON-RPC transport for persistent agent connections
- New CLI commands: `dream`, `notify`, `link`, `unlink`, `resolve-links`, `backlinks`
- Updated `serve` to start daemon + socket server

### Future Considerations
- SQLite + FTS5 for improved search performance at scale
- Compress task (summarize verbose memories)
- gRPC/HTTP transport for remote agent access
- Dream level 3 with LLM-assisted classification (currently heuristic-only)
