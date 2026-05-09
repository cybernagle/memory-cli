# Memory CLI — Project Memory

Quick-reference index for project knowledge. See linked files for details.

## Architecture & Design

- File-based storage with YAML frontmatter in Markdown files (not SQLite)
- Atomic writes via `.tmp` + `os.Rename`
- **Phase/Category model**: `inbox` → `organized` with 14 categories (replaced old short-term/long-term)
- Category directories: `~/.memory/categories/{inbox,people,knowledge,...}/`
- Bidirectional wikilinks: `[[link]]` syntax, `ResolveBacklinks`, `LinkMemories`/`UnlinkMemories`
- In-memory search with keyword/tag/date/category filtering
- Dream engine with 3 levels (light/medium/deep), idle detection
- Notification system: macOS `osascript` + `pending.md` for agent startup
- Unix socket JSON-RPC transport at `~/.memory/memory.sock`
- Agent framework (inspired by pi-mono + car-agent):
  - `Tool` interface with 7 memory tools (6 CRUD + smart_search)
  - `Agent` with registry, hooks (before/after tool call), event broadcasting
  - `Session` with context injection (auto-search relevant memories)
  - Three communication methods: CLI, stdin JSON-RPC, Unix socket
- Daemon runs 6 tasks on interval: expire, decay, upgrade, consolidate, dream, notify
- 5 ingestion adapters: claude, car-agent, fingersaver, logseq, obsidian

## Key Decisions

- **File-based over SQLite**: Chose markdown files with YAML frontmatter for human-readability and git-friendliness. SQLite+FTS5 is a future consideration for scale.
- **No CGO constraint**: Uses pure Go libraries only.
- **Content hash deduplication**: SHA256 hash in frontmatter prevents duplicate ingestion.
- **Phase/Category over Type**: Replaced `short/long` type with `inbox/organized` phase + 14 categories. Backward compat: `readFromFile` handles legacy YAML with `type: short/long`.
- **Bidirectional links**: Links are symmetric — linking A→B also links B→A. Wikilinks extracted from content with `ExtractWikiLinks()`.
- **Heuristic classification**: Dream engine uses keyword heuristics (Chinese + English) for auto-classification. LLM-assisted classification is a future consideration.
- **Import cycle resolution**: `config.CategoryDir` takes `string` instead of `store.Category` to avoid config→store→config cycle.

## Implementation Notes

- `store.Write` (7 args: content, phase, category, scope, tags, source) creates file atomically
- `store.Read` increments `access_count` on every read
- `store.Upgrade` moves file from inbox to category directory with rollback on failure
- Search uses `strings.Contains` (case-insensitive) — no ranking or stemming
- Consolidate deduplicates by first 100 chars of content, keeps highest access_count
- Duration parser supports day-based format: `"30d"` → 30 × 24h, `"168h"` → 7 days
- Daemon lock file at `~/.memory/.daemon.lock` contains PID
- Dream levels: Light (classify inbox), Medium (+ merge + wikilinks), Deep (+ reminders)
- IdleDetector thresholds: Light=5min, Medium=30min, Deep=2h
- Socket server uses 1MB buffer per connection, supports `tools/list`, `tools/call`, direct tool names

## Test Coverage

- `internal/store/store_test.go` — CRUD, TTL, list filters, search, upgrade, hash dedup
- `internal/daemon/daemon_test.go` — expire, decay, consolidate, RunOnce
- `internal/cmd/cmd_test.go` — write/read round-trip, list, search, category validation
- `internal/agent/agent_test.go` — Execute, hooks (before/after), Subscribe, ListTools, ExecuteAll
- `internal/agent/session_test.go` — RunJSON, multi-step, InjectContext, error handling

## Module

`github.com/cybernagle/memory-cli` — Go 1.26.2

Dependencies: `cobra`, `yaml.v3`, `google/uuid`
