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

Memories progress through two phases across 14 categories:

```
inbox → organized (classified into a category)
```

```
~/.memory/
├── config.yaml
├── memory.sock
├── memory.md
├── pending.md
└── categories/
    ├── inbox/          # New, unclassified
    ├── people/         # People and relationships
    ├── project/        # Projects, repos, deployments
    ├── knowledge/      # Technical knowledge, patterns
    ├── preferences/    # User/agent preferences
    ├── feedback/       # Feedback and suggestions
    ├── decisions/      # Design decisions
    ├── lessons/        # Lessons learned, gotchas
    ├── habits/         # Behavioral patterns
    ├── skills/         # Skills and capabilities
    ├── date/           # Deadlines, schedules
    ├── soul/           # Core identity, values
    ├── character/      # Personality traits
    └── reminders/      # Time-based reminders
```

Each memory is a Markdown file with YAML frontmatter:

```markdown
---
id: 550e8400-e29b-41d4-a716-446655440000
phase: organized
category: preferences
tags: [preference, ui]
source: claude
access_count: 3
links: [7f8d9e00-...]
---
user prefers dark mode
```

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

```bash
memory link <id1> <id2>          # Create bidirectional link
memory unlink <id1> <id2>        # Remove link
memory resolve-links             # Resolve [[wikilinks]] in content
memory backlinks <id>            # Show incoming links
```

Memories can reference each other with `[[wikilink]]` syntax. `resolve-links` scans all content, extracts wikilinks, and updates link metadata bidirectionally.

### Dream Engine

```bash
memory dream --level 1   # Light: classify inbox into categories
memory dream --level 2   # Medium: + merge similar + resolve wikilinks
memory dream --level 3   # Deep: + extract reminders from time expressions
```

Classification supports both Chinese and English heuristics:
- "偏好"/"prefers" → `preferences`
- "教训"/"learned" → `lessons`
- "决定"/"decided" → `decisions`
- etc.

### Notifications

```bash
memory notify
```

Checks due reminders and pushes macOS notifications. Writes pending reminders to `~/.memory/pending.md` for agents to consume on startup.

### Daemon

```bash
memory serve [--interval 60s]
```

Starts background daemon (expire, decay, upgrade, consolidate) plus Unix socket server at `~/.memory/memory.sock`.

### Lifecycle

```bash
memory upgrade <id>       # inbox → organized
memory decay              # Remove unused organized memories
memory consolidate        # Run all daemon tasks once
```

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

### Method 2: stdin JSON-RPC

For LLM agents that output JSON tool calls:

```bash
# List available tools
memory agent --list-tools

# Execute tools via stdin
echo '[{"name":"memory_write","params":{"content":"hello","category":"knowledge"}}]' | memory agent

# Context-aware prompt
echo '{"prompt":"user preferences about UI"}' | memory agent --prompt -
```

7 tools available: `memory_write`, `memory_read`, `memory_delete`, `memory_list`, `memory_search`, `memory_tag`, `memory_smart_search`

### Method 3: Unix Socket

Persistent connection when daemon is running:

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

```
┌──────────────────────────────────────────────────────────────┐
│  CLI (Cobra) │ JSON-RPC stdin │ Unix Socket                  │
├──────────────────────────────────────────────────────────────┤
│  Agent Framework — 7 tools, hooks, events, session           │
├──────────────────────────────────────────────────────────────┤
│  Store — YAML frontmatter files, bidirectional links          │
├──────────────────────────────────────────────────────────────┤
│  Daemon — expire, decay, upgrade, consolidate, dream, notify │
└──────────────────────────────────────────────────────────────┘
```

## Development

```bash
go build -o memory .
go test ./...
go vet ./...
go fmt ./...
```

## License

MIT
