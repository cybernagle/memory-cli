# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Project Vision

**Memory CLI** is a Go CLI tool that provides unified memory management for multiple AI agents (Claude Code, Copilot, FingerSaver, etc.). Agents read, write, search, and consolidate memories through a command-line interface. A background daemon continuously processes and refines stored memories.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Memory CLI (Cobra)                   │
│  write / read / delete / list / search / tag / ingest │
├─────────────────────────────────────────────────────┤
│                    Store Layer                        │
│  SQLite (WAL mode) + FTS5                            │
│  ~/.memory/short-term/  ~/.memory/long-term/          │
│  ~/.memory/index.db                                  │
├─────────────────────────────────────────────────────┤
│                Ingestion Adapters                     │
│  Claude | Car-Agent | FingerSaver | Logseq | Obsidian│
├─────────────────────────────────────────────────────┤
│                 Background Daemon                     │
│  Expire | Decay | Consolidate | Compress | Link       │
└─────────────────────────────────────────────────────┘
```

## Build & Dev Commands

```bash
go build -o memory .
go run . write "user prefers dark mode" --tags preference,ui
go run . read <id>
go run . search "dark mode"
go run . list --type short
go run . ingest --source claude
go run . serve
go test ./...
go vet ./...
go fmt ./...
```

## Technical Constraints

- Go 1.26+, no CGO
- SQLite via `modernc.org/sqlite` (pure Go)
- Cobra for CLI framework
- YAML config via `gopkg.in/yaml.v3`
- Storage root: `~/.memory/`

## Key Packages

| Package | Purpose |
|---------|---------|
| `internal/store` | SQLite storage, CRUD operations, FTS5 search |
| `internal/ingest` | Ingestion adapters for various sources |
| `internal/daemon` | Background processing tasks |
| `internal/config` | Config loading from `~/.memory/config.yaml` |
| `internal/cmd` | Cobra command definitions |

## Pre-Commit Checklist

- [ ] `go vet ./...` passes
- [ ] `gofmt -l .` shows no unformatted files
- [ ] `go test ./...` passes
- [ ] No API keys or sensitive data in test fixtures
