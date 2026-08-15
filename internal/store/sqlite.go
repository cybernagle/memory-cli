package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/cybernagle/memory-cli/internal/config"
)

type SqliteStore struct {
	db             *sql.DB
	searchStrategy string // "idf"(default) | "bm25" | "hybrid"
}

func (s *SqliteStore) DB() *sql.DB { return s.db }

func NewSqliteStore(dbPath string) (*SqliteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &SqliteStore{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func NewSqliteStoreFromConfig(cfg *config.Config) (*SqliteStore, error) {
	dbPath := cfg.Storage.SQLitePath
	if dbPath == "" {
		dbPath = config.SQLiteDefaultPath()
	}
	s, err := NewSqliteStore(dbPath)
	if err != nil {
		return nil, err
	}
	// Apply search strategy from config ("idf" default, "bm25", "hybrid").
	if cfg.Search.Strategy != "" {
		s.searchStrategy = cfg.Search.Strategy
	} else {
		s.searchStrategy = "idf"
	}
	return s, nil
}

func (s *SqliteStore) Init() error { return s.init() }

func (s *SqliteStore) init() error {
	if _, err := s.db.Exec(sqliteSchema); err != nil {
		return err
	}
	// Migrate: add session_id column if missing
	s.db.Exec("ALTER TABLE memories ADD COLUMN session_id TEXT NOT NULL DEFAULT ''")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id)")

	// Migrate: recreate links table without target_id FK (wiki-links are text labels, not memory IDs)
	s.migrateLinksTable()

	// Migrate: add processed_by column for per-processor consumption tracking
	s.db.Exec("ALTER TABLE memories ADD COLUMN processed_by TEXT NOT NULL DEFAULT '[]'")
	s.db.Exec("UPDATE memories SET processed_by = '[]' WHERE processed_by IS NULL")

	// Migrate: add raw_entry_id column linking each memory to its append-only raw_entries source.
	s.db.Exec("ALTER TABLE memories ADD COLUMN raw_entry_id TEXT")

	// Migrate: add project column (cwd basename anchor) for provenance by project.
	s.db.Exec("ALTER TABLE memories ADD COLUMN project TEXT")
	s.db.Exec("UPDATE memories SET project = '' WHERE project IS NULL")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project)")

	// Migrate: add tmux_session column for provenance by tmux session (the writer process
	// runs inside tmux, so $TMUX is set). Idempotent ALTER — error discarded on re-run.
	s.db.Exec("ALTER TABLE memories ADD COLUMN tmux_session TEXT")
	s.db.Exec("UPDATE memories SET tmux_session = '' WHERE tmux_session IS NULL")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_tmux ON memories(tmux_session)")

	// Migrate: add consumed_mask (bitmask of consumers that have processed this memory).
	// Each consumer owns a bit; marking is a single atomic `mask | bit` UPDATE.
	s.db.Exec("ALTER TABLE memories ADD COLUMN consumed_mask INTEGER NOT NULL DEFAULT 0")
	s.db.Exec("UPDATE memories SET consumed_mask = 0 WHERE consumed_mask IS NULL")

	// Migrate: ingest-time provenance enrichment. These columns capture the conversation
	// context a memory came from, so downstream aggregation can thread/group by them.
	// Each ALTER is idempotent — SQLite errors "duplicate column name" on re-run and the
	// error is discarded, matching the existing migration pattern in this init().
	s.db.Exec("ALTER TABLE memories ADD COLUMN message_uuid TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE memories ADD COLUMN parent_uuid TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE memories ADD COLUMN role TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE memories ADD COLUMN git_branch TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE memories ADD COLUMN model TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE memories ADD COLUMN prompt_id TEXT NOT NULL DEFAULT ''")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_prompt ON memories(prompt_id)")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_parent_uuid ON memories(parent_uuid)")

	// metadata column: free-form JSON for proposal status, profile evidence, etc.
	// SQLite-only (FileStore does not persist this). Default '{}' so existing rows are valid.
	s.db.Exec("ALTER TABLE memories ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'")

	// Backfill the append-only raw_entries from existing memories and link them.
	// Idempotent: INSERT OR IGNORE dedups by content_hash (PK); the UPDATE is a no-op once raw_entry_id is set.
	// This guarantees every memory — including those written before raw capture existed — has a permanent source.
	s.db.Exec(`INSERT OR IGNORE INTO raw_entries (id, content, source, content_hash)
		SELECT content_hash, content, source, content_hash
		FROM memories WHERE content_hash != ''`)
	s.db.Exec(`UPDATE memories SET raw_entry_id = content_hash
		WHERE raw_entry_id IS NULL AND content_hash != ''`)

	// Migrate: fatten the raw_entries event log with provenance columns (idempotent ALTERs —
	// SQLite errors "duplicate column name" on re-run and the error is discarded, matching
	// the existing migration pattern). New DBs get them from the schema const.
	for _, col := range []string{
		"session_id", "project", "tmux_session", "message_uuid", "parent_uuid",
		"role", "git_branch", "model", "prompt_id",
	} {
		s.db.Exec("ALTER TABLE raw_entries ADD COLUMN " + col + " TEXT NOT NULL DEFAULT ''")
	}
	s.backfillRawEntryProvenance()

	// Migrate: rebuild memories_fts with the trigram tokenizer if it still uses unicode61.
	// unicode61 can't segment CJK, forcing multi-word/Chinese queries onto SearchLike's
	// full-table LIKE scan. Trigram indexes every ≥3-char substring (DDIA ch4, n-gram
	// inverted index), and also accelerates LIKE '%kw%' on the FTS table itself.
	s.migrateFTSTokenizer()

	return nil
}

func (s *SqliteStore) Close() error {
	return s.db.Close()
}

// migrateFTSTokenizer rebuilds memories_fts if it still uses the unicode61 tokenizer.
// Idempotent: after the rebuild the CREATE statement in sqlite_master contains 'trigram',
// so subsequent inits are a no-op. Backfill mirrors InsertMemory's FTS write exactly
// (tags joined by spaces) so search behavior is identical before/after migration.
// backfillRawEntryProvenance copies provenance from memories into legacy event rows
// (written before the event log carried provenance). Idempotent: only empty event columns
// are filled, and it skips entirely once no row needs it.
func (s *SqliteStore) backfillRawEntryProvenance() {
	var needs bool
	s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM raw_entries WHERE session_id='' AND project=''
		AND tmux_session='' AND message_uuid='' AND prompt_id='')`).Scan(&needs)
	if !needs {
		return
	}
	s.db.Exec(`UPDATE raw_entries AS r SET
		session_id   = CASE WHEN r.session_id=''   AND m.session_id!=''   THEN m.session_id   ELSE r.session_id END,
		project      = CASE WHEN r.project=''      AND m.project!=''      THEN m.project      ELSE r.project END,
		tmux_session = CASE WHEN r.tmux_session='' AND m.tmux_session!='' THEN m.tmux_session ELSE r.tmux_session END,
		message_uuid = CASE WHEN r.message_uuid='' AND m.message_uuid!='' THEN m.message_uuid ELSE r.message_uuid END,
		parent_uuid  = CASE WHEN r.parent_uuid=''  AND m.parent_uuid!=''  THEN m.parent_uuid  ELSE r.parent_uuid END,
		role         = CASE WHEN r.role=''         AND m.role!=''         THEN m.role         ELSE r.role END,
		git_branch   = CASE WHEN r.git_branch=''   AND m.git_branch!=''   THEN m.git_branch   ELSE r.git_branch END,
		model        = CASE WHEN r.model=''        AND m.model!=''        THEN m.model        ELSE r.model END,
		prompt_id    = CASE WHEN r.prompt_id=''    AND m.prompt_id!=''    THEN m.prompt_id    ELSE r.prompt_id END
	FROM memories m
	WHERE m.raw_entry_id = r.id`)
}

func (s *SqliteStore) migrateFTSTokenizer() {
	var createSQL string
	err := s.db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='memories_fts'").Scan(&createSQL)
	if err != nil || createSQL == "" {
		return // no FTS table (fresh DB — schema const already creates it with trigram)
	}
	if !strings.Contains(createSQL, "unicode61") {
		return // already trigram
	}
	s.rebuildFTS()
}

func (s *SqliteStore) migrateLinksTable() {
	var count int
	row := s.db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_list('links') WHERE field = 'target_id'")
	row.Scan(&count)
	if count == 0 {
		return // already migrated
	}
	// Disable FK checks during migration
	s.db.Exec("PRAGMA foreign_keys=OFF")
	s.db.Exec(`CREATE TABLE links_new (
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		PRIMARY KEY (source_id, target_id),
		FOREIGN KEY (source_id) REFERENCES memories(id) ON DELETE CASCADE
	)`)
	s.db.Exec("INSERT OR IGNORE INTO links_new SELECT source_id, target_id FROM links")
	s.db.Exec("DROP TABLE links")
	s.db.Exec("ALTER TABLE links_new RENAME TO links")
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_links_target ON links(target_id)")
	s.db.Exec("PRAGMA foreign_keys=ON")
}

func (s *SqliteStore) WriteToInbox(content string, category Category, scope string, tags []string, source string, project string, tmuxSession string) (*Memory, error) {
	ttl, err := parseDuration("168h")
	if err != nil {
		ttl = 168 * time.Hour
	}
	expires := time.Now().Add(ttl)
	mem := &Memory{
		Content:     content,
		Phase:       PhaseInbox,
		Category:    defaultCategory(category),
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		Project:     project,
		TmuxSession: tmuxSession,
		ExpiresAt:   &expires,
		Links:       ExtractWikiLinks(content),
	}
	// Route through IngestMemory (the unified chokepoint) so ID/hash/timestamp defaults + any
	// write-time side effects (supersede for fact phases) apply uniformly. Inbox writes don't
	// supersede, but going through one entry point keeps all write paths consistent.
	if err := s.IngestMemory(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *SqliteStore) Write(content string, memType Phase, category Category, scope string, tags []string, source string) (*Memory, error) {
	now := time.Now()
	mem := &Memory{
		Content:     content,
		Phase:       memType,
		Category:    category,
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		CreatedAt:   now,
		UpdatedAt:   now,
		Version:     1,
		Links:       ExtractWikiLinks(content),
	}
	if memType == PhaseInbox {
		ttl, err := parseDuration("168h")
		if err != nil {
			return nil, fmt.Errorf("invalid ttl: %w", err)
		}
		expires := now.Add(ttl)
		mem.ExpiresAt = &expires
	}
	// Route through IngestMemory so organized/processed writes (facts) trigger supersede
	// automatically, and defaults are filled uniformly. This replaces the old direct
	// InsertMemory call that bypassed supersede entirely.
	if err := s.IngestMemory(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *SqliteStore) Read(id string) (*Memory, error) {
	// Resolve prefix → full ID: search/list display truncated IDs (e.g. "58e541e0") but the
	// user/agent may pass only that prefix. Accept it if it uniquely matches one memory.
	fullID, err := s.resolveID(id)
	if err != nil {
		return nil, err
	}
	mem, err := s.FindByID(fullID)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("UPDATE memories SET access_count = access_count + 1, updated_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), fullID)
	if err != nil {
		return nil, err
	}
	mem.AccessCount++
	return mem, nil
}

func (s *SqliteStore) Delete(id string) error {
	fullID, err := s.resolveID(id)
	if err != nil {
		return err
	}
	s.db.Exec("DELETE FROM memories_fts WHERE memory_id = ?", fullID)
	_, err = s.db.Exec("DELETE FROM memories WHERE id = ?", fullID)
	return err
}

func (s *SqliteStore) List(opts ListOptions) ([]*Memory, error) {
	query := "SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata FROM memories WHERE 1=1"
	var args []any

	if opts.Category != "" {
		query += " AND category = ?"
		args = append(args, string(opts.Category))
	}
	if opts.Phase != "" {
		query += " AND phase = ?"
		args = append(args, string(opts.Phase))
	}
	if opts.Scope != "" {
		query += " AND scope = ?"
		args = append(args, opts.Scope)
	}
	if opts.Source != "" {
		query += " AND source = ?"
		args = append(args, opts.Source)
	}
	if opts.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, opts.SessionID)
	}
	if opts.Project != "" {
		query += " AND project = ?"
		args = append(args, opts.Project)
	}
	if opts.PromptID != "" {
		query += " AND prompt_id = ?"
		args = append(args, opts.PromptID)
	}
	if opts.CreatedAfter != nil {
		query += " AND created_at > ?"
		args = append(args, opts.CreatedAfter.Format(time.RFC3339))
	}
	if opts.CreatedBefore != nil {
		query += " AND created_at < ?"
		args = append(args, opts.CreatedBefore.Format(time.RFC3339))
	}

	query += " ORDER BY created_at DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
		if opts.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, opts.Offset)
		}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	var rawMemories []*Memory
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		ids = append(ids, mem.ID)
		rawMemories = append(rawMemories, mem)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	tagMap, err := s.batchLoadTags(ids)
	if err != nil {
		return rawMemories, nil
	}

	linkMap, err := s.batchLoadLinks(ids)
	if err != nil {
		return rawMemories, nil
	}

	for _, mem := range rawMemories {
		mem.Tags = tagMap[mem.ID]
		if mem.Tags == nil {
			mem.Tags = []string{}
		}
		mem.Links = linkMap[mem.ID]
		if mem.Links == nil {
			mem.Links = []string{}
		}
	}

	if len(opts.Tags) > 0 {
		var filtered []*Memory
		for _, mem := range rawMemories {
			if hasAllTags(mem.Tags, opts.Tags) {
				filtered = append(filtered, mem)
			}
		}
		return filtered, nil
	}

	return rawMemories, nil
}

func (s *SqliteStore) Tag(id string, add, remove []string) (*Memory, error) {
	mem, err := s.FindByID(id)
	if err != nil {
		return nil, err
	}
	tagSet := make(map[string]bool)
	for _, t := range mem.Tags {
		tagSet[t] = true
	}
	for _, t := range add {
		tagSet[t] = true
	}
	for _, t := range remove {
		delete(tagSet, t)
	}
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec("DELETE FROM tags WHERE memory_id = ?", id)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	for _, t := range tags {
		_, err = tx.Exec("INSERT INTO tags (memory_id, tag) VALUES (?, ?)", id, t)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	_, err = tx.Exec("UPDATE memories SET updated_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), id)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	mem.Tags = tags
	mem.UpdatedAt = time.Now()
	return mem, nil
}

func (s *SqliteStore) Upgrade(id string) error {
	mem, err := s.FindByID(id)
	if err != nil {
		return err
	}
	if mem.Phase == PhaseOrganized {
		return nil
	}
	_, err = s.db.Exec("UPDATE memories SET phase = 'organized', expires_at = NULL, updated_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), id)
	return err
}

func (s *SqliteStore) MarkProcessed(id string) error {
	_, err := s.db.Exec("UPDATE memories SET phase = 'processed', expires_at = NULL, updated_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), id)
	return err
}

// MarkConsumed records that a consumer has processed a memory, using a single atomic
// bitwise-OR update on consumed_mask. Because the read-modify-write happens inside one
// SQL statement, concurrent consumers can never lose each other's marks (no race).
// Unknown consumer names are a no-op (logged by callers if needed). The legacy
// processed_by column is no longer written — consumed_mask is the source of truth.
func (s *SqliteStore) MarkConsumed(id string, processorName string) error {
	c, ok := ConsumerByName(processorName)
	if !ok {
		return nil
	}
	res, err := s.db.Exec("UPDATE memories SET consumed_mask = consumed_mask | ?, updated_at = ? WHERE id = ?",
		int64(c), time.Now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	// Log a no-op so a stale/deleted id doesn't fail silently. Still idempotent: re-marking an
	// already-consumed row updates 1 row (the bit is already set but updated_at flips), so this
	// only fires for genuinely-missing ids.
	if n, _ := res.RowsAffected(); n == 0 {
		log.Printf("[store] MarkConsumed(%s, %s): no rows matched (id missing or deleted)", id, processorName)
	}
	return nil
}

// ListUnconsumedInPhase returns memories in the given phases that have NOT been consumed by the
// named processor, ordered oldest-first (so backlog drains in chronological order). Unlike
// ListUnconsumed (which is inbox-only), this reaches organized/processed memories — needed by
// EntityExtractionTask, whose backlog lives in those phases and was previously unreachable
// because the task queried List(Limit:500) by created_at DESC (newest-first, never reaching
// the 11k of older backlog). limit caps the result; pass a large number for a one-off drain.
func (s *SqliteStore) ListUnconsumedInPhase(processorName string, phases []Phase, limit int) ([]*Memory, error) {
	c, ok := ConsumerByName(processorName)
	if !ok {
		return nil, fmt.Errorf("unknown consumer: %s", processorName)
	}
	if limit <= 0 {
		limit = 100
	}
	if len(phases) == 0 {
		phases = []Phase{PhaseOrganized, PhaseProcessed}
	}
	placeholders := make([]string, len(phases))
	args := []any{int64(c)}
	for i, p := range phases {
		placeholders[i] = "?"
		args = append(args, string(p))
	}
	args = append(args, limit)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, content, content_hash, phase, category, scope, source, session_id,
		       created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask,
		       message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata
		FROM memories
		WHERE (consumed_mask & ?) = 0 AND phase IN (%s)
		ORDER BY created_at ASC
		LIMIT ?`, strings.Join(placeholders, ",")),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Memory
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		out = append(out, mem)
	}
	return out, nil
}

func (s *SqliteStore) ListUnconsumed(processorName string) ([]*Memory, error) {
	c, ok := ConsumerByName(processorName)
	if !ok {
		return nil, nil
	}
	// TODO(code-review): phase='inbox' is hardcoded. This is intentional for the fact-processor
	// flow (inbox → processed is the only path the plugin pipeline consumes), but it means a
	// processor registered for processed/organized memories would never see them here. If a
	// future processor needs a non-inbox phase, parameterize the phase filter rather than
	// loosening this default.
	rows, err := s.db.Query(`
		SELECT id, content, content_hash, phase, category, scope, source, session_id,
		       created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask,
		       message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata
		FROM memories
		WHERE phase = 'inbox' AND (consumed_mask & ?) = 0
		ORDER BY created_at ASC`,
		int64(c))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	var rawMemories []*Memory
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		ids = append(ids, mem.ID)
		rawMemories = append(rawMemories, mem)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	tagMap, _ := s.batchLoadTags(ids)
	linkMap, _ := s.batchLoadLinks(ids)

	for _, mem := range rawMemories {
		mem.Tags = tagMap[mem.ID]
		if mem.Tags == nil {
			mem.Tags = []string{}
		}
		mem.Links = linkMap[mem.ID]
		if mem.Links == nil {
			mem.Links = []string{}
		}
	}

	return rawMemories, nil
}

// resolveID accepts a full UUID or a unique prefix (e.g. the truncated IDs shown by
// search/list). If `id` matches exactly, it is returned unchanged. Otherwise it looks for a
// memory whose ID starts with the prefix; if exactly one matches, that full ID is returned.
// Ambiguous or no matches return an error.
//
// This lets users/agents pass "58e541e0" instead of "58e541e0-d74b-4e50-9fa0-b5452d4501f08".
func (s *SqliteStore) resolveID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("%w: empty id", ErrNotFound)
	}
	// Exact match first (covers full UUIDs and any exact id).
	var exact string
	s.db.QueryRow("SELECT id FROM memories WHERE id = ?", id).Scan(&exact)
	if exact != "" {
		return exact, nil
	}
	// Prefix match — must be unique.
	rows, err := s.db.Query("SELECT id FROM memories WHERE id LIKE ?", id+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var match string
	count := 0
	for rows.Next() {
		var rid string
		rows.Scan(&rid)
		match = rid
		count++
	}
	switch count {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrNotFound, id)
	case 1:
		return match, nil
	default:
		return "", fmt.Errorf("ambiguous id prefix %q: matches %d memories", id, count)
	}
}

func (s *SqliteStore) FindByID(id string) (*Memory, error) {
	row := s.db.QueryRow(
		"SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata FROM memories WHERE id = ?",
		id)
	mem, err := scanMemoryRowSingle(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	tags, err := s.loadTags(id)
	if err != nil {
		return nil, err
	}
	mem.Tags = tags
	links, err := s.loadLinks(id)
	if err != nil {
		return nil, err
	}
	mem.Links = links
	return mem, nil
}

func (s *SqliteStore) FindByHash(hash string) (*Memory, error) {
	row := s.db.QueryRow(
		"SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata FROM memories WHERE content_hash = ?",
		hash)
	mem, err := scanMemoryRowSingle(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	tags, _ := s.loadTags(mem.ID)
	mem.Tags = tags
	links, _ := s.loadLinks(mem.ID)
	mem.Links = links
	return mem, nil
}

// splitQueryKeywords splits a raw query into lowercase keywords. Two delimiter families:
//   - " OR " / "|"  → produced by LLM keyword extraction ("keyword1 OR keyword2")
//   - whitespace / comma → produced by multi-word user queries ("橘粒科技 合同 报价")
func splitQueryKeywords(query string) []string {
	keywordStr := strings.ToLower(query)
	keywordStr = strings.ReplaceAll(keywordStr, " or ", "|")
	keywords := strings.FieldsFunc(keywordStr, func(r rune) bool {
		return r == '|' || r == ' ' || r == '\t' || r == ',' || r == '，'
	})
	for i, k := range keywords {
		keywords[i] = strings.TrimSpace(k)
	}
	return keywords
}

// buildTrigramMatch converts a raw query into an FTS5 MATCH expression usable with the
// trigram tokenizer: each keyword becomes a quoted phrase, keywords joined with OR —
// mirroring SearchLike's any-keyword-matches semantics, with ranking (bm25 or
// created_at) supplying precision. Trigram can only match substrings of ≥3 codepoints,
// so if ANY keyword is shorter (e.g. "合同") it returns "" and callers fall back to
// SearchLike rather than silently dropping that keyword from the match set.
func buildTrigramMatch(query string) string {
	keywords := splitQueryKeywords(query)
	if len(keywords) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if len([]rune(kw)) < 3 {
			return ""
		}
		// Escape embedded double quotes so the FTS phrase syntax stays valid.
		parts = append(parts, `"`+strings.ReplaceAll(kw, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " OR ")
}

func (s *SqliteStore) Search(opts SearchOptions) ([]*Memory, error) {
	if opts.Query == "" {
		return s.List(ListOptions{
			Phase: opts.Phase,
			Scope: opts.Scope,
			Tags:  opts.Tags,
			Limit: 100,
		})
	}

	// Trigram tokenizer: CJK and multi-word queries can now use the FTS inverted index.
	// buildTrigramMatch splits the query into keywords (same splitting as SearchLike) and
	// wraps each as an FTS phrase joined with OR — preserving SearchLike's OR semantics,
	// with bm25/created_at handling precision via ranking. Trigram can only match
	// substrings of ≥3 codepoints, so if ANY keyword is shorter (e.g. "合同"), the whole
	// query falls back to SearchLike (the pre-trigram path) for correctness.
	match := buildTrigramMatch(opts.Query)
	if match == "" {
		return s.SearchLike(opts)
	}
	// ftsOpts carries the MATCH expression for FTS paths; opts keeps the raw query for
	// SearchLike (which re-splits keywords itself and must not see the MATCH syntax).
	ftsOpts := opts
	ftsOpts.Query = match

	// Strategy dispatch: hybrid fuses BM25+IDF via RRF; bm25/idf delegate to searchFTS.
	// searchFTS receives ftsOpts (MATCH expression); searchHybrid receives both so its
	// SearchLike arm keeps splitting the raw query.
	if s.searchStrategy == "hybrid" {
		return s.searchHybrid(opts, ftsOpts)
	}
	return s.searchFTS(ftsOpts, s.searchStrategy == "bm25")
}

// searchFTS runs the FTS5 query built by Search, with optional BM25 relevance ordering.
// Extracted so searchHybrid can call it without re-entering the strategy switch (recursion).
func (s *SqliteStore) searchFTS(opts SearchOptions, bm25 bool) ([]*Memory, error) {
	query := `
		SELECT m.id, m.content, m.content_hash, m.phase, m.category, m.scope, m.source,
		       m.session_id, m.created_at, m.updated_at, m.expires_at, m.access_count, m.version, m.processed_by, m.project, m.tmux_session, m.consumed_mask,
		       m.message_uuid, m.parent_uuid, m.role, m.git_branch, m.model, m.prompt_id, m.metadata
		FROM memories m
		WHERE m.id IN (
			SELECT memory_id FROM memories_fts WHERE memories_fts MATCH ?
		)`
	args := []interface{}{opts.Query}
	if opts.Phase != "" {
		query += " AND m.phase = ?"
		args = append(args, string(opts.Phase))
	}
	if opts.Scope != "" {
		query += " AND m.scope = ?"
		args = append(args, opts.Scope)
	}
	if opts.Project != "" {
		query += " AND m.project = ?"
		args = append(args, opts.Project)
	}
	if opts.SessionID != "" {
		query += " AND m.session_id = ?"
		args = append(args, opts.SessionID)
	}
	if opts.Category != "" {
		query += " AND m.category = ?"
		args = append(args, string(opts.Category))
	}
	if opts.From != nil {
		query += " AND m.created_at >= ?"
		args = append(args, opts.From.Format(time.RFC3339))
	}
	if opts.To != nil {
		query += " AND m.created_at <= ?"
		args = append(args, opts.To.Format(time.RFC3339))
	}
	if bm25 {
		query = strings.Replace(query,
			"SELECT memory_id FROM memories_fts WHERE memories_fts MATCH ?",
			"SELECT memory_id FROM memories_fts WHERE memories_fts MATCH ? ORDER BY bm25(memories_fts)", 1)
	} else {
		query += " ORDER BY m.created_at DESC"
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return s.SearchLike(opts)
	}
	defer rows.Close()

	var results []*Memory
	var ids []string
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		results = append(results, mem)
		ids = append(ids, mem.ID)
	}
	if len(ids) == 0 {
		return s.SearchLike(opts)
	}

	tagMap, _ := s.batchLoadTags(ids)
	linkMap, _ := s.batchLoadLinks(ids)
	for _, mem := range results {
		mem.Tags = tagMap[mem.ID]
		if mem.Tags == nil {
			mem.Tags = []string{}
		}
		mem.Links = linkMap[mem.ID]
		if mem.Links == nil {
			mem.Links = []string{}
		}
	}
	results = s.filterSearch(results, opts)
	return results, nil
}

// searchHybrid runs both FTS (BM25-ranked) and SearchLike (IDF-ranked), then fuses the two
// ranked lists via Reciprocal Rank Fusion (RRF). RRF doesn't need score normalization
// (BM25 and IDF have different scales) — it only uses rank positions:
//
//	RRF_score(doc) = Σ 1/(k + rank_in_each_list)   where k=60 (standard constant)
//
// This combines BM25's strength (trigram substring matching, term frequency saturation) with
// IDF+LIKE's strength (short CJK keywords + entity prefix fallback). A doc ranked #1 in
// IDF but #50 in BM25 still scores well; a doc ranked #1 in both dominates.
// ftsOpts carries the prebuilt MATCH expression; opts carries the raw query for SearchLike.
func (s *SqliteStore) searchHybrid(opts SearchOptions, ftsOpts SearchOptions) ([]*Memory, error) {
	// Run FTS directly (NOT via s.Search to avoid recursion — Search would re-enter
	// searchHybrid). We build the FTS query inline with BM25 ordering.
	ftsResults, _ := s.searchFTS(ftsOpts, true) // true = BM25 ordering

	// Run SearchLike (IDF + CJK prefix + boost).
	likeResults, _ := s.SearchLike(opts)

	// Fuse via RRF.
	const rrfK = 60.0
	type entry struct {
		mem   *Memory
		score float64
	}
	merged := make(map[string]*entry)

	addRank := func(results []*Memory) {
		for rank, m := range results {
			if _, ok := merged[m.ID]; !ok {
				merged[m.ID] = &entry{mem: m}
			}
			merged[m.ID].score += 1.0 / (rrfK + float64(rank+1))
		}
	}
	addRank(ftsResults)
	addRank(likeResults)

	// Collect and sort by RRF score descending.
	all := make([]*entry, 0, len(merged))
	for _, e := range merged {
		all = append(all, e)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	out := make([]*Memory, len(all))
	for i, e := range all {
		out[i] = e.mem
	}
	return out, nil
}

func (s *SqliteStore) SearchLike(opts SearchOptions) ([]*Memory, error) {
	// Split the query into keywords via the shared splitter (same families as FTS path):
	//   - " OR " / "|"  → produced by LLM keyword extraction ("keyword1 OR keyword2")
	//   - whitespace     → produced by multi-word user queries ("橘粒科技 合同 报价 项目")
	// CJK entity names like "瑞福莱暖通设备" never contain spaces, so whitespace-splitting
	// does not break them — the IDF + CJK-prefix logic below still treats each such token
	// as one indivisible keyword.
	keywords := splitQueryKeywords(opts.Query)

	// Compute total doc count for IDF denominator.
	var totalDocs int
	s.db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&totalDocs)
	if totalDocs == 0 {
		totalDocs = 1
	}

	// Compute IDF weight per keyword: weight = log(totalDocs / docsContainingKeyword).
	// Rare keywords → high weight. Common keywords → near-zero weight.
	//
	// For CJK entity keywords (e.g. "瑞福莱暖通设备（上海）有限公司"), the full string rarely
	// matches verbatim — content usually has a shorter form ("瑞福莱"). So we precompute a
	// fallback prefix: the longest CJK prefix that exists in the corpus, with its own IDF.
	// This MUST be computed once per keyword here, NOT inside the per-memory loop — otherwise
	// it becomes an N×K full-table-scan (13k memories × LIKE '%us%' = tens of minutes), which
	// hangs the MCP server on any English keyword like "user".
	//
	// ASCII keywords skip prefix expansion entirely: "us"/"use" are meaningless substrings and
	// matching them is both useless (no entity signal) and catastrophically slow.
	idf := func(docCount int) float64 {
		if docCount <= 0 {
			return 0
		}
		w := math.Log(float64(totalDocs) / float64(docCount))
		if w < 0.1 {
			w = 0.1 // floor: even common keywords contribute a little
		}
		return w
	}
	kwWeight := make(map[string]float64)     // full-keyword IDF
	prefixWeight := make(map[string]float64) // best-matching CJK-prefix IDF (fallback)
	prefixStr := make(map[string]string)     // the actual prefix substring to test per-memory
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		var cnt int
		// IDF probe. ≥3-char patterns probe the trigram-indexed FTS table (indexed LIKE,
		// DDIA ch4 n-gram inverted index); shorter patterns keep the old memories scan —
		// trigram can't accelerate them, and the FTS table is larger than memories.
		if len([]rune(kw)) >= 3 {
			s.db.QueryRow("SELECT COUNT(*) FROM memories_fts WHERE content LIKE ?", "%"+kw+"%").Scan(&cnt)
		} else {
			s.db.QueryRow("SELECT COUNT(*) FROM memories WHERE lower(content) LIKE ?", "%"+kw+"%").Scan(&cnt)
		}
		if cnt > 0 {
			kwWeight[kw] = idf(cnt)
		}
		// Note: we do NOT skip when cnt==0. For a CJK keyword whose full form matches nothing
		// (e.g. "瑞福莱暖通设备有限公司" when content only has "瑞福莱"), the prefix fallback below
		// is exactly what recovers the match. Skipping here would defeat the whole feature.

		// CJK-only prefix fallback: walk progressively shorter prefixes (longest first),
		// pick the first one that exists in the corpus. ASCII keywords get no fallback —
		// "us"/"use" are meaningless substrings and matching them is both useless (no entity
		// signal) and catastrophically slow (the original N+1 hang).
		if !containsCJK(kw) {
			continue
		}
		runes := []rune(kw)
		for n := len(runes) - 1; n >= 2; n-- {
			sub := string(runes[:n])
			var subCnt int
			if n >= 3 {
				s.db.QueryRow("SELECT COUNT(*) FROM memories_fts WHERE content LIKE ?", "%"+sub+"%").Scan(&subCnt)
			} else {
				s.db.QueryRow("SELECT COUNT(*) FROM memories WHERE lower(content) LIKE ?", "%"+sub+"%").Scan(&subCnt)
			}
			if subCnt > 0 {
				prefixWeight[kw] = idf(subCnt)
				prefixStr[kw] = sub
				break
			}
		}
	}

	// Candidate fetch: load only rows matching at least one keyword or its precomputed CJK
	// prefix fallback — the trigram index accelerates each ≥3-char LIKE, replacing the old
	// full-corpus s.List() load + in-Go filtering. Short patterns stay correct via plain
	// LIKE, just without index acceleration. Scoring below is unchanged.
	var subq strings.Builder
	subq.WriteString("SELECT memory_id FROM memories_fts WHERE ")
	candArgs := make([]interface{}, 0, 2*len(keywords))
	seenPat := make(map[string]bool, 2*len(keywords))
	addPat := func(pat string) {
		if pat == "" || seenPat[pat] {
			return
		}
		seenPat[pat] = true
		if len(candArgs) > 0 {
			subq.WriteString(" OR ")
		}
		subq.WriteString("content LIKE ?")
		candArgs = append(candArgs, "%"+pat+"%")
	}
	for _, kw := range keywords {
		addPat(kw)
		addPat(prefixStr[kw])
	}
	query := `
		SELECT m.id, m.content, m.content_hash, m.phase, m.category, m.scope, m.source,
		       m.session_id, m.created_at, m.updated_at, m.expires_at, m.access_count, m.version, m.processed_by, m.project, m.tmux_session, m.consumed_mask,
		       m.message_uuid, m.parent_uuid, m.role, m.git_branch, m.model, m.prompt_id, m.metadata
		FROM memories m
		WHERE m.id IN (` + subq.String() + `) AND m.phase != ''
	`
	if opts.Phase != "" {
		query = strings.Replace(query, "AND m.phase != ''", "AND m.phase = ?", 1)
		candArgs = append(candArgs, string(opts.Phase))
	} else {
		query = strings.Replace(query, " AND m.phase != ''", "", 1)
	}
	if opts.Scope != "" {
		query += " AND m.scope = ?"
		candArgs = append(candArgs, opts.Scope)
	}
	query += " ORDER BY m.created_at DESC"

	rows, err := s.db.Query(query, candArgs...)
	if err != nil {
		return nil, err
	}
	var memories []*Memory
	var ids []string
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		memories = append(memories, mem)
		ids = append(ids, mem.ID)
	}
	rows.Close()
	tagMap, _ := s.batchLoadTags(ids)
	linkMap, _ := s.batchLoadLinks(ids)
	for _, mem := range memories {
		mem.Tags = tagMap[mem.ID]
		if mem.Tags == nil {
			mem.Tags = []string{}
		}
		mem.Links = linkMap[mem.ID]
		if mem.Links == nil {
			mem.Links = []string{}
		}
	}

	type scored struct {
		mem   *Memory
		score float64
	}
	var results []scored
	// The first keyword from LLM extraction is usually the most specific entity name
	// (e.g. "瑞福莱暖通设备"). Give it a 2x boost so memories matching it dominate over
	// memories matching only generic trailing keywords (e.g. "细节", IDF≈5.6 ≈ 瑞福莱 6.2).
	firstKeywordIdx := -1
	for i, kw := range keywords {
		if strings.TrimSpace(kw) != "" {
			firstKeywordIdx = i
			break
		}
	}

	// Build an O(1) lookup for excluded sources so the per-memory loop stays cheap.
	excludedSources := make(map[string]bool, len(opts.ExcludeSources))
	for _, src := range opts.ExcludeSources {
		excludedSources[src] = true
	}

	for _, mem := range memories {
		// Drop auto-generated aggregates (evidence/profile signal) from semantic search results.
		// They're statistical fitting signal for the brain, not user-facing facts, and their
		// constantly-updated timestamps let them crowd out real memories in recency ranking.
		if excludedSources[mem.Source] {
			continue
		}
		contentLower := strings.ToLower(mem.Content)
		score := 0.0
		for kwi, kw := range keywords {
			if kw == "" {
				continue
			}
			// Skip a keyword only if it matches nothing AND has no CJK prefix fallback.
			// A CJK entity ("瑞福莱暖通设备有限公司") may have kwWeight=0 (no verbatim match)
			// yet still score via its "瑞福莱" prefix — that's the prefix feature's whole point.
			if kwWeight[kw] == 0 && prefixStr[kw] == "" {
				continue
			}
			boost := 1.0
			if kwi == firstKeywordIdx {
				boost = 2.0 // first keyword = primary entity, double its weight
			}
			if strings.Contains(contentLower, kw) {
				score += kwWeight[kw] * boost
				continue
			}
			// Full keyword didn't match — try the precomputed CJK prefix (if any).
			// ASCII keywords have no prefix fallback. This is an in-memory substring test,
			// so the whole per-memory loop is O(1) DB queries regardless of corpus size.
			if psub := prefixStr[kw]; psub != "" && strings.Contains(contentLower, psub) {
				score += prefixWeight[kw] * boost
			}
		}
		if score > 0 {
			results = append(results, scored{mem: mem, score: score})
		}
	}
	// Sort by IDF-weighted score descending — rare-keyword matches float to the top.
	sort.SliceStable(results, func(i, j int) bool { return results[i].score > results[j].score })

	out := make([]*Memory, len(results))
	for i, r := range results {
		out[i] = r.mem
	}
	out = s.filterSearch(out, opts)
	return out, nil
}

// containsCJK reports whether s contains any CJK ideograph. Used by SearchLike to decide
// whether a keyword is eligible for CJK prefix expansion (ASCII keywords like "user" are not).
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

func (s *SqliteStore) filterSearch(memories []*Memory, opts SearchOptions) []*Memory {
	// Tags require a join on the tags table, so they're still filtered post-fetch.
	// Time/project/session/category/phase/scope filters are pushed into SQL in Search().
	var results []*Memory
	for _, mem := range memories {
		if len(opts.Tags) > 0 && !hasAllTags(mem.Tags, opts.Tags) {
			continue
		}
		results = append(results, mem)
	}
	return results
}

func (s *SqliteStore) ResolveBacklinks() (int, error) {
	all, err := s.List(ListOptions{})
	if err != nil {
		return 0, err
	}

	titleIndex := make(map[string]*Memory)
	for _, mem := range all {
		title := titleFromContent(mem.Content)
		if title != "" {
			titleIndex[strings.ToLower(title)] = mem
		}
	}

	count := 0
	for _, mem := range all {
		wikiLinks := ExtractWikiLinks(mem.Content)
		if len(wikiLinks) == 0 {
			continue
		}
		changed := false
		for _, link := range wikiLinks {
			target, ok := titleIndex[strings.ToLower(link)]
			if !ok || target.ID == mem.ID {
				continue
			}
			if !s.linkExists(mem.ID, target.ID) {
				if err := s.insertLink(mem.ID, target.ID); err != nil {
					continue
				}
				changed = true
			}
			if !s.linkExists(target.ID, mem.ID) {
				if err := s.insertLink(target.ID, mem.ID); err != nil {
					continue
				}
				changed = true
			}
		}
		if changed {
			count++
		}
	}
	return count, nil
}

func (s *SqliteStore) GetBacklinks(id string) ([]*Memory, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.content, m.content_hash, m.phase, m.category, m.scope, m.source,
		       m.session_id, m.created_at, m.updated_at, m.expires_at, m.access_count, m.version, m.processed_by, m.project, m.tmux_session, m.consumed_mask,
		       m.message_uuid, m.parent_uuid, m.role, m.git_branch, m.model, m.prompt_id, m.metadata
		FROM links l
		JOIN memories m ON m.id = l.source_id
		WHERE l.target_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*Memory
	var ids []string
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		results = append(results, mem)
		ids = append(ids, mem.ID)
	}

	tagMap, _ := s.batchLoadTags(ids)
	linkMap, _ := s.batchLoadLinks(ids)
	for _, mem := range results {
		mem.Tags = tagMap[mem.ID]
		mem.Links = linkMap[mem.ID]
		if mem.Tags == nil {
			mem.Tags = []string{}
		}
		if mem.Links == nil {
			mem.Links = []string{}
		}
	}
	return results, nil
}

func (s *SqliteStore) LinkMemories(sourceID, targetID string) error {
	if _, err := s.FindByID(sourceID); err != nil {
		return fmt.Errorf("source not found: %w", err)
	}
	if _, err := s.FindByID(targetID); err != nil {
		return fmt.Errorf("target not found: %w", err)
	}
	if err := s.insertLink(sourceID, targetID); err != nil {
		return err
	}
	return s.insertLink(targetID, sourceID)
}

func (s *SqliteStore) UnlinkMemories(sourceID, targetID string) error {
	if _, err := s.FindByID(sourceID); err != nil {
		return err
	}
	if _, err := s.FindByID(targetID); err != nil {
		return err
	}
	_, err := s.db.Exec("DELETE FROM links WHERE (source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?)",
		sourceID, targetID, targetID, sourceID)
	return err
}

func (s *SqliteStore) LogActivity(action, memoryID, source, detail string) error {
	_, err := s.db.Exec("INSERT INTO activity_log (action, memory_id, source, detail) VALUES (?, ?, ?, ?)",
		action, memoryID, source, detail)
	return err
}

func (s *SqliteStore) QueryRows(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(query, args...)
}

// Stats helpers

func (s *SqliteStore) TotalCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
	return count, err
}

func (s *SqliteStore) Backup(dstPath string) error {
	_, err := s.db.Exec("VACUUM INTO ?", dstPath)
	return err
}

// DistinctSessions returns unique session_ids from inbox memories.
func (s *SqliteStore) DistinctSessions() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT session_id FROM memories WHERE phase = 'inbox' AND session_id != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			continue
		}
		sessions = append(sessions, sid)
	}
	return sessions, nil
}

// ResetToInbox resets all organized memories back to inbox.
func (s *SqliteStore) ResetToInbox() (int, error) {
	result, err := s.db.Exec("UPDATE memories SET phase = 'inbox', expires_at = datetime('now', '+7 days') WHERE phase = 'organized'")
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// InboxCount returns the number of inbox memories.
func (s *SqliteStore) InboxCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM memories WHERE phase = 'inbox'").Scan(&count)
	return count, err
}

// RawEntryCount returns the number of rows in the append-only raw_entries table.
// Useful for verifying that raw capture is working and that nothing is ever deleted.
func (s *SqliteStore) RawEntryCount() (int64, error) {
	var count int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM raw_entries").Scan(&count)
	return count, err
}

// Internal helpers

func (s *SqliteStore) InsertMemory(mem *Memory) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	pbData, _ := json.Marshal(mem.ProcessedBy)
	if mem.ProcessedBy == nil || string(pbData) == "null" {
		pbData = []byte("[]")
	}

	// Append-only raw capture: every memory is recorded in raw_entries and is never deleted.
	// content_hash is the primary key, so INSERT OR IGNORE dedups identical content idempotently
	// (the FIRST event keeps its sequence; provenance below backfills onto the existing row).
	rawHash := mem.ContentHash
	if rawHash == "" {
		rawHash = HashContent(mem.Content)
	}
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO raw_entries
			(id, content, source, content_hash, session_id, project, tmux_session,
			 message_uuid, parent_uuid, role, git_branch, model, prompt_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rawHash, mem.Content, mem.Source, rawHash,
		mem.SessionID, mem.Project, mem.TmuxSession,
		mem.MessageUUID, mem.ParentUUID, mem.Role, mem.GitBranch, mem.Model, mem.PromptID,
	); err != nil {
		tx.Rollback()
		return err
	}
	// Dedup hit (identical content seen before): the event keeps its original rowid/sequence,
	// but accumulate any provenance this occurrence captured that the event lacks.
	if _, err = tx.Exec(`UPDATE raw_entries SET
			session_id   = CASE WHEN session_id=''   AND ?!=''   THEN ? ELSE session_id END,
			project      = CASE WHEN project=''      AND ?!=''      THEN ? ELSE project END,
			tmux_session = CASE WHEN tmux_session='' AND ?!='' THEN ? ELSE tmux_session END,
			message_uuid = CASE WHEN message_uuid='' AND ?!='' THEN ? ELSE message_uuid END,
			parent_uuid  = CASE WHEN parent_uuid=''  AND ?!=''  THEN ? ELSE parent_uuid END,
			role         = CASE WHEN role=''         AND ?!=''         THEN ? ELSE role END,
			git_branch   = CASE WHEN git_branch=''   AND ?!=''   THEN ? ELSE git_branch END,
			model        = CASE WHEN model=''        AND ?!=''        THEN ? ELSE model END,
			prompt_id    = CASE WHEN prompt_id=''    AND ?!=''    THEN ? ELSE prompt_id END
			WHERE id = ?`,
		mem.SessionID, mem.SessionID,
		mem.Project, mem.Project,
		mem.TmuxSession, mem.TmuxSession,
		mem.MessageUUID, mem.MessageUUID,
		mem.ParentUUID, mem.ParentUUID,
		mem.Role, mem.Role,
		mem.GitBranch, mem.GitBranch,
		mem.Model, mem.Model,
		mem.PromptID, mem.PromptID,
		rawHash); err != nil {
		tx.Rollback()
		return err
	}

	// Auto-categorize at the chokepoint: when no real category was given (the default inbox),
	// derive one from content. Real categories (explicit user/LLM) are preserved. Uses a local
	// so the caller's mem.Category is not mutated as a side effect.
	categoryVal := mem.Category
	if categoryVal == CategoryInbox {
		categoryVal = CategorizeContent(mem.Content)
	}

	// Upsert by content_hash: if an identical-content memory already exists, do NOT insert a
	// duplicate. Instead, backfill any provenance columns that are empty on the existing row
	// (role, git_branch, message_uuid, parent_uuid, model, prompt_id, session_id, project) from
	// the incoming memory. This makes re-ingest safe (no duplicates) AND progressively enriches
	// older memories with provenance captured by newer ingest code. Non-provenance fields
	// (phase, tags, processed_by, consumed_mask) are deliberately left untouched — the existing
	// memory's processing state is authoritative.
	var existingID string
	err = tx.QueryRow("SELECT id FROM memories WHERE content_hash = ? LIMIT 1", mem.ContentHash).Scan(&existingID)
	if err == nil && existingID != "" {
		// Existing memory found — update only the provenance columns, preserving prior values
		// where the incoming memory carries richer info than what's stored. Each `SET col = ?
		// WHERE col = ''` keeps any non-empty existing value, so re-ingest never clobbers.
		if _, err = tx.Exec(`UPDATE memories SET
				session_id   = CASE WHEN session_id   = '' AND ? != '' THEN ? ELSE session_id END,
				project      = CASE WHEN project      = '' AND ? != '' THEN ? ELSE project END,
				tmux_session = CASE WHEN tmux_session = '' AND ? != '' THEN ? ELSE tmux_session END,
				message_uuid = CASE WHEN message_uuid = '' AND ? != '' THEN ? ELSE message_uuid END,
				parent_uuid  = CASE WHEN parent_uuid  = '' AND ? != '' THEN ? ELSE parent_uuid END,
				role         = CASE WHEN role         = '' AND ? != '' THEN ? ELSE role END,
				git_branch   = CASE WHEN git_branch   = '' AND ? != '' THEN ? ELSE git_branch END,
				model        = CASE WHEN model        = '' AND ? != '' THEN ? ELSE model END,
				prompt_id    = CASE WHEN prompt_id    = '' AND ? != '' THEN ? ELSE prompt_id END
			WHERE id = ?`,
			mem.SessionID, mem.SessionID,
			mem.Project, mem.Project,
			mem.TmuxSession, mem.TmuxSession,
			mem.MessageUUID, mem.MessageUUID,
			mem.ParentUUID, mem.ParentUUID,
			mem.Role, mem.Role,
			mem.GitBranch, mem.GitBranch,
			mem.Model, mem.Model,
			mem.PromptID, mem.PromptID,
			existingID); err != nil {
			tx.Rollback()
			return err
		}
		// Adopt the existing ID so the caller's mem reflects the row we touched, and re-enrich
		// tags (new ingest may carry new provenance tags like "subagent" the old row lacks).
		mem.ID = existingID
		tagsToInsert := mergeTags(mem.Tags, ExtractKeywords(mem.Content))
		for _, tag := range tagsToInsert {
			if _, err = tx.Exec("INSERT OR IGNORE INTO tags (memory_id, tag) VALUES (?, ?)", mem.ID, tag); err != nil {
				tx.Rollback()
				return err
			}
		}
		return tx.Commit()
	}
	// No existing memory (or query error) → fall through to a fresh INSERT.

	_, err = tx.Exec(`INSERT INTO memories (id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, raw_entry_id, project, tmux_session, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mem.ID, mem.Content, mem.ContentHash, string(mem.Phase), string(categoryVal),
		mem.Scope, mem.Source, mem.SessionID,
		mem.CreatedAt.Format(time.RFC3339), mem.UpdatedAt.Format(time.RFC3339),
		formatTime(mem.ExpiresAt), mem.AccessCount, mem.Version, string(pbData), rawHash, mem.Project, mem.TmuxSession, mem.ConsumedMask,
		mem.MessageUUID, mem.ParentUUID, mem.Role, mem.GitBranch, mem.Model, mem.PromptID, metadataJSON(mem.Metadata))
	if err != nil {
		tx.Rollback()
		return err
	}

	// Auto-enrich tags with high-precision keywords extracted from content (free, no LLM).
	// Done at the single insert chokepoint so every memory — regardless of entry path — gets
	// semantic tags, not just provenance tags from the caller. Uses a local slice so the
	// caller's mem.Tags is not mutated as a side effect.
	tagsToInsert := mergeTags(mem.Tags, ExtractKeywords(mem.Content))

	for _, tag := range tagsToInsert {
		_, err = tx.Exec("INSERT OR IGNORE INTO tags (memory_id, tag) VALUES (?, ?)", mem.ID, tag)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	// Wiki-links are resolved later by ResolveBacklinks()

	tagsStr := strings.Join(tagsToInsert, " ")
	_, err = tx.Exec("INSERT INTO memories_fts (memory_id, content, tags, scope, source) VALUES (?, ?, ?, ?, ?)",
		mem.ID, mem.Content, tagsStr, mem.Scope, mem.Source)
	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *SqliteStore) loadTags(memoryID string) ([]string, error) {
	rows, err := s.db.Query("SELECT tag FROM tags WHERE memory_id = ?", memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			continue
		}
		tags = append(tags, tag)
	}
	if tags == nil {
		tags = []string{}
	}
	return tags, nil
}

func (s *SqliteStore) loadLinks(memoryID string) ([]string, error) {
	rows, err := s.db.Query("SELECT target_id FROM links WHERE source_id = ?", memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []string
	for rows.Next() {
		var link string
		if err := rows.Scan(&link); err != nil {
			continue
		}
		links = append(links, link)
	}
	if links == nil {
		links = []string{}
	}
	return links, nil
}

func (s *SqliteStore) batchLoadTags(ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query("SELECT memory_id, tag FROM tags WHERE memory_id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var memID, tag string
		if err := rows.Scan(&memID, &tag); err != nil {
			continue
		}
		result[memID] = append(result[memID], tag)
	}
	return result, nil
}

func (s *SqliteStore) batchLoadLinks(ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query("SELECT source_id, target_id FROM links WHERE source_id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var srcID, tgtID string
		if err := rows.Scan(&srcID, &tgtID); err != nil {
			continue
		}
		result[srcID] = append(result[srcID], tgtID)
	}
	return result, nil
}

func (s *SqliteStore) insertLink(sourceID, targetID string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO links (source_id, target_id) VALUES (?, ?)", sourceID, targetID)
	return err
}

func (s *SqliteStore) linkExists(sourceID, targetID string) bool {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM links WHERE source_id = ? AND target_id = ?", sourceID, targetID).Scan(&count)
	return count > 0
}

func scanMemoryRow(rows *sql.Rows) (*Memory, error) {
	var mem Memory
	var phase, category, scope, source, sessionID, project string
	var createdAt, updatedAt string
	var expiresAt sql.NullString
	var processedBy string
	var metadataStr string
	var tmuxSession sql.NullString
	err := rows.Scan(&mem.ID, &mem.Content, &mem.ContentHash, &phase, &category,
		&scope, &source, &sessionID, &createdAt, &updatedAt, &expiresAt, &mem.AccessCount, &mem.Version, &processedBy, &project, &tmuxSession, &mem.ConsumedMask,
		&mem.MessageUUID, &mem.ParentUUID, &mem.Role, &mem.GitBranch, &mem.Model, &mem.PromptID, &metadataStr)
	if err != nil {
		return nil, err
	}
	if tmuxSession.Valid {
		mem.TmuxSession = tmuxSession.String
	}
	mem.Phase = Phase(phase)
	mem.Category = Category(category)
	mem.Scope = scope
	mem.Source = source
	mem.SessionID = sessionID
	mem.Project = project
	mem.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	mem.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if expiresAt.Valid && expiresAt.String != "" {
		t, err := time.Parse(time.RFC3339, expiresAt.String)
		if err == nil {
			mem.ExpiresAt = &t
		}
	}
	json.Unmarshal([]byte(processedBy), &mem.ProcessedBy)
	if mem.ProcessedBy == nil {
		mem.ProcessedBy = []string{}
	}
	if metadataStr != "" && metadataStr != "{}" {
		json.Unmarshal([]byte(metadataStr), &mem.Metadata)
	}
	return &mem, nil
}

func scanMemoryRowSingle(row *sql.Row) (*Memory, error) {
	var mem Memory
	var phase, category, scope, source, sessionID, project string
	var createdAt, updatedAt string
	var expiresAt sql.NullString
	var processedBy string
	var metadataStr string
	var tmuxSession sql.NullString
	err := row.Scan(&mem.ID, &mem.Content, &mem.ContentHash, &phase, &category,
		&scope, &source, &sessionID, &createdAt, &updatedAt, &expiresAt, &mem.AccessCount, &mem.Version, &processedBy, &project, &tmuxSession, &mem.ConsumedMask,
		&mem.MessageUUID, &mem.ParentUUID, &mem.Role, &mem.GitBranch, &mem.Model, &mem.PromptID, &metadataStr)
	if err != nil {
		return nil, err
	}
	if tmuxSession.Valid {
		mem.TmuxSession = tmuxSession.String
	}
	mem.Phase = Phase(phase)
	mem.Category = Category(category)
	mem.Scope = scope
	mem.Source = source
	mem.SessionID = sessionID
	mem.Project = project
	mem.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	mem.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if expiresAt.Valid && expiresAt.String != "" {
		t, err := time.Parse(time.RFC3339, expiresAt.String)
		if err == nil {
			mem.ExpiresAt = &t
		}
	}
	json.Unmarshal([]byte(processedBy), &mem.ProcessedBy)
	if mem.ProcessedBy == nil {
		mem.ProcessedBy = []string{}
	}
	if metadataStr != "" && metadataStr != "{}" {
		json.Unmarshal([]byte(metadataStr), &mem.Metadata)
	}
	return &mem, nil
}

func formatTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

// IngestMemory is the unified write chokepoint: every memory enters storage through here. It
// fills defaults (ID/hash/timestamps/version) then inserts. Critically, it triggers supersede
// detection AFTER the insert commits — so version tracking (marking older memories of the same
// fact as superseded) is an atomic side-effect of writing, not a manual call only one of the
// 17 write paths remembered to make (chaos point ②). Supersede only fires for
// organized/processed memories (facts that can have versions); inbox/capture writes are raw
// events that never supersede anything.
//
// The supersede runs after tx.Commit (not inside InsertMemory's transaction) because
// CheckAndSupersede lists existing memories and MarkSuperseded writes via s.db — running them
// on the same tx would risk re-entrancy/deadlock, and the new memory must be committed before
// it can be the supersede target.
// MaxContentLen guards against garbage writes (pasted logs, accidental dumps). Events are
// immutable and never deleted, so a garbage write is permanent — reject before appending.
const MaxContentLen = 100 * 1024

// Command-gate errors (DDIA ch3: a request is a command; only a validated command becomes
// an event). Rejected commands leave NO trace in raw_entries.
var (
	ErrEmptyContent    = errors.New("memory content is empty")
	ErrContentTooLarge = fmt.Errorf("memory content exceeds %d bytes", MaxContentLen)
	ErrInvalidPhase    = errors.New("invalid memory phase")
)

// normalizeTags trims, drops empties and dedups, preserving first-seen order.
func normalizeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// IsValidCategory reports whether cat is one of the known categories.
func IsValidCategory(cat Category) bool {
	for _, c := range append(append(AllCategories, CategoryInbox), CategoryReminders) {
		if c == cat {
			return true
		}
	}
	return false
}

func (s *SqliteStore) IngestMemory(mem *Memory) error {
	// ── Command gate ──
	// Validate and normalize BEFORE the event is appended: raw_entries is append-only and
	// never deleted, so anything that passes here is permanent. Trim happens before any
	// hashing so dedup stays consistent (same trimmed content → same hash).
	mem.Content = strings.TrimSpace(mem.Content)
	if mem.Content == "" {
		return ErrEmptyContent
	}
	if len(mem.Content) > MaxContentLen {
		return ErrContentTooLarge
	}
	switch mem.Phase {
	case PhaseInbox, PhaseProcessed, PhaseOrganized:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPhase, mem.Phase)
	}
	// Unknown categories fall back to inbox so the auto-categorizer decides, rather than
	// persisting a category no downstream consumer knows about.
	if mem.Category != "" && mem.Category != CategoryInbox && !IsValidCategory(mem.Category) {
		mem.Category = CategoryInbox
	}
	mem.Tags = normalizeTags(mem.Tags)

	if mem.ID == "" {
		mem.ID = uuid.New().String()
	}
	if mem.ContentHash == "" {
		mem.ContentHash = HashContent(mem.Content)
	}
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = time.Now()
	}
	if mem.UpdatedAt.IsZero() {
		mem.UpdatedAt = mem.CreatedAt
	}
	if mem.Version == 0 {
		mem.Version = 1
	}
	if err := s.InsertMemory(mem); err != nil {
		return err
	}
	// Post-commit supersede: if this is a fact (organized/processed), check whether it makes
	// an older version of the same fact obsolete. Return value (count) is intentionally ignored
	// — supersession is best-effort bookkeeping; a failure here must not fail the write.
	if mem.Phase == PhaseOrganized || mem.Phase == PhaseProcessed {
		s.CheckAndSupersede(mem)
	}
	return nil
}

// metadataJSON serializes a Memory's Metadata map to a JSON string for storage.
// Returns "{}" for nil/empty maps so the column is always valid JSON.
func metadataJSON(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
