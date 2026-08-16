package store

const sqliteSchema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS memories (
    id           TEXT PRIMARY KEY,
    content      TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    phase        TEXT NOT NULL DEFAULT 'inbox',
    category     TEXT NOT NULL DEFAULT 'inbox',
    scope        TEXT NOT NULL DEFAULT 'global',
    source       TEXT NOT NULL DEFAULT 'manual',
    session_id   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    expires_at   TEXT,
    access_count  INTEGER NOT NULL DEFAULT 0,
    version       INTEGER NOT NULL DEFAULT 1,
    processed_by  TEXT    NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_memories_phase ON memories(phase);
CREATE INDEX IF NOT EXISTS idx_memories_category ON memories(category);
CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope);
CREATE INDEX IF NOT EXISTS idx_memories_source ON memories(source);
CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id);
CREATE INDEX IF NOT EXISTS idx_memories_hash ON memories(content_hash);
CREATE INDEX IF NOT EXISTS idx_memories_expires ON memories(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memories_phase_processed ON memories(phase, processed_by);

CREATE TABLE IF NOT EXISTS tags (
    memory_id TEXT NOT NULL,
    tag       TEXT NOT NULL,
    PRIMARY KEY (memory_id, tag),
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tags_tag ON tags(tag);

CREATE TABLE IF NOT EXISTS links (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    PRIMARY KEY (source_id, target_id),
    FOREIGN KEY (source_id) REFERENCES memories(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_links_target ON links(target_id);

-- The append-only event log (DDIA ch3 event sourcing). Carries full provenance so the
-- derived views (memories/tags/links/fts) can be rebuilt from events alone. rowid is the
-- monotonic event sequence; id (= content_hash) dedups identical content idempotently.
CREATE TABLE IF NOT EXISTS raw_entries (
    id           TEXT PRIMARY KEY,
    content      TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'manual',
    ingested_at  TEXT NOT NULL DEFAULT (datetime('now')),
    content_hash TEXT NOT NULL,
    session_id   TEXT NOT NULL DEFAULT '',
    project      TEXT NOT NULL DEFAULT '',
    tmux_session TEXT NOT NULL DEFAULT '',
    message_uuid TEXT NOT NULL DEFAULT '',
    parent_uuid  TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    git_branch   TEXT NOT NULL DEFAULT '',
    model        TEXT NOT NULL DEFAULT '',
    prompt_id    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_raw_entries_hash ON raw_entries(content_hash);

CREATE TABLE IF NOT EXISTS activity_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    action     TEXT NOT NULL,
    memory_id  TEXT,
    source     TEXT NOT NULL DEFAULT 'cli',
    detail     TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_activity_created ON activity_log(created_at);
CREATE INDEX IF NOT EXISTS idx_activity_action ON activity_log(action);

-- Reminders are event-driven triggers, separate from knowledge memories. A reminder carries
-- a trigger_at timestamp and a status lifecycle (pending → fired → done). This keeps the
-- temporary task queue from polluting the permanent fact store.
CREATE TABLE IF NOT EXISTS reminders (
    id         TEXT PRIMARY KEY,
    content    TEXT NOT NULL,
    trigger_at TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL,
    fired_at   TEXT,
    source     TEXT NOT NULL DEFAULT 'cli'
);
CREATE INDEX IF NOT EXISTS idx_reminders_trigger ON reminders(trigger_at);
CREATE INDEX IF NOT EXISTS idx_reminders_status ON reminders(status);

-- session_views: the per-session projection of the event log (CQRS read model #2).
-- One row per session_id, (re)built by the daemon's SessionDigestTask: what task the
-- session performed, which entity+facet it revolved around, a summary, and extracted
-- reusable lessons. LLM-derived (non-deterministic); rebuilt on demand, never the
-- source of truth.
CREATE TABLE IF NOT EXISTS session_views (
    session_id   TEXT PRIMARY KEY,
    project      TEXT NOT NULL DEFAULT '',
    tmux_session TEXT NOT NULL DEFAULT '',
    first_seen   TEXT NOT NULL DEFAULT '',
    last_seen    TEXT NOT NULL DEFAULT '',
    memory_count INTEGER NOT NULL DEFAULT 0,
    task         TEXT NOT NULL DEFAULT '',
    entity       TEXT NOT NULL DEFAULT '',
    facet        TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    lessons      TEXT NOT NULL DEFAULT '[]',
    model        TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_session_views_project ON session_views(project);
CREATE INDEX IF NOT EXISTS idx_session_views_entity ON session_views(entity);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    memory_id UNINDEXED,
    content,
    tags,
    scope,
    source,
    tokenize='trigram'
);
`
