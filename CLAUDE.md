# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Project Vision

**Memory CLI** is a Go CLI tool that provides unified memory management for multiple AI agents (Claude Code, Copilot, FingerSaver, etc.). Agents read, write, search, and consolidate memories through CLI, JSON-RPC stdin, or Unix socket. A background daemon continuously processes and refines stored memories using a progressive disclosure model (inbox → organized) with category-based classification.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     Consumer Layer                            │
│  CLI (Cobra) | JSON-RPC stdin | Unix Socket                  │
│  write/read/delete/list/search/tag/ingest/dream/notify/agent │
├──────────────────────────────────────────────────────────────┤
│                     Agent Framework                           │
│  Session → Agent → Tool Registry (7 memory tools)            │
│  Hooks (before/after) | Events (broadcast)                   │
├──────────────────────────────────────────────────────────────┤
│                   File-Based Store                            │
│  YAML frontmatter in Markdown files                          │
│  ~/.memory/categories/{inbox,people,knowledge,...}            │
│  Atomic writes (.tmp + rename) | Bidirectional links          │
├──────────────────────────────────────────────────────────────┤
│                   Ingestion Adapters                          │
│  Claude | Car-Agent | FingerSaver | Logseq | Obsidian        │
├──────────────────────────────────────────────────────────────┤
│                   Background Daemon                           │
│  Expire | Decay | Upgrade | Consolidate | Dream | Notify     │
└──────────────────────────────────────────────────────────────┘
```

> See [MEMORY.md](MEMORY.md) for project knowledge, design decisions, and implementation notes.
> See [SPEC.md](SPEC.md) for full specification.

## Build & Dev Commands

```bash
go build -o memory .
go run . write "user prefers dark mode" --category preferences --tags preference,ui
go run . read <id>
go run . search "dark mode"
go run . list --category knowledge
go run . link <id1> <id2>
go run . resolve-links
go run . dream --level 2
go run . notify
go run . serve
go run . ingest --source claude
go run . agent --list-tools
echo '[{"name":"memory_write","params":{"content":"hello","category":"knowledge"}}]' | go run . agent
go test ./...
go vet ./...
go fmt ./...
```

## Technical Constraints

- Go 1.26+, no CGO
- Cobra for CLI framework
- YAML config via `gopkg.in/yaml.v3`
- Storage root: `~/.memory/`
- Categories: `~/.memory/categories/{inbox,people,knowledge,...}/`

## Key Packages

| Package | Purpose |
|---------|---------|
| `internal/store` | File-based storage, CRUD, search, bidirectional links |
| `internal/agent` | Agent framework: Tool interface, events, hooks, session, 7 tools |
| `internal/transport` | Unix socket JSON-RPC server |
| `internal/ingest` | Ingestion adapters for various sources |
| `internal/daemon` | Background processing: expire, decay, upgrade, consolidate, dream, notify |
| `internal/config` | Config loading from `~/.memory/config.yaml` |
| `internal/cmd` | Cobra command definitions |

## Pre-Commit Checklist

- [ ] `go vet ./...` passes
- [ ] `gofmt -l .` shows no unformatted files
- [ ] `go test ./...` passes
- [ ] No API keys or sensitive data in test fixtures
