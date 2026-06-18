package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/cybernagle/memory-cli/internal/config"
)

type SqliteStore struct {
	db *sql.DB
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
	return NewSqliteStore(dbPath)
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

	// Backfill the append-only raw_entries from existing memories and link them.
	// Idempotent: INSERT OR IGNORE dedups by content_hash (PK); the UPDATE is a no-op once raw_entry_id is set.
	// This guarantees every memory — including those written before raw capture existed — has a permanent source.
	s.db.Exec(`INSERT OR IGNORE INTO raw_entries (id, content, source, content_hash)
		SELECT content_hash, content, source, content_hash
		FROM memories WHERE content_hash != ''`)
	s.db.Exec(`UPDATE memories SET raw_entry_id = content_hash
		WHERE raw_entry_id IS NULL AND content_hash != ''`)

	return nil
}

func (s *SqliteStore) Close() error {
	return s.db.Close()
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

func (s *SqliteStore) WriteToInbox(content string, scope string, tags []string, source string, project string) (*Memory, error) {
	now := time.Now()
	ttl, err := parseDuration("168h")
	if err != nil {
		ttl = 168 * time.Hour
	}
	expires := now.Add(ttl)
	mem := &Memory{
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: HashContent(content),
		Phase:       PhaseInbox,
		Category:    CategoryInbox,
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		Project:     project,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   &expires,
		Version:     1,
		Links:       ExtractWikiLinks(content),
	}
	if err := s.InsertMemory(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *SqliteStore) Write(content string, memType Phase, category Category, scope string, tags []string, source string) (*Memory, error) {
	now := time.Now()
	mem := &Memory{
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: HashContent(content),
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
	if err := s.InsertMemory(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *SqliteStore) Read(id string) (*Memory, error) {
	mem, err := s.FindByID(id)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("UPDATE memories SET access_count = access_count + 1, updated_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	mem.AccessCount++
	return mem, nil
}

func (s *SqliteStore) Delete(id string) error {
	s.db.Exec("DELETE FROM memories_fts WHERE memory_id = ?", id)
	_, err := s.db.Exec("DELETE FROM memories WHERE id = ?", id)
	return err
}

func (s *SqliteStore) List(opts ListOptions) ([]*Memory, error) {
	query := "SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id FROM memories WHERE 1=1"
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
	_, err := s.db.Exec("UPDATE memories SET consumed_mask = consumed_mask | ?, updated_at = ? WHERE id = ?",
		int64(c), time.Now().Format(time.RFC3339), id)
	return err
}

func (s *SqliteStore) ListUnconsumed(processorName string) ([]*Memory, error) {
	c, ok := ConsumerByName(processorName)
	if !ok {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, content, content_hash, phase, category, scope, source, session_id,
		       created_at, updated_at, expires_at, access_count, version, processed_by, project, consumed_mask,
		       message_uuid, parent_uuid, role, git_branch, model, prompt_id
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

func (s *SqliteStore) FindByID(id string) (*Memory, error) {
	row := s.db.QueryRow(
		"SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id FROM memories WHERE id = ?",
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
		"SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id FROM memories WHERE content_hash = ?",
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

func (s *SqliteStore) Search(opts SearchOptions) ([]*Memory, error) {
	if opts.Query == "" {
		return s.List(ListOptions{
			Phase: opts.Phase,
			Scope: opts.Scope,
			Tags:  opts.Tags,
			Limit: 100,
		})
	}

	// Try FTS5 first, with space/time filters pushed into SQL (not post-fetch Go filtering).
	// Building the WHERE dynamically so each set filter is a real indexed predicate.
	query := `
		SELECT m.id, m.content, m.content_hash, m.phase, m.category, m.scope, m.source,
		       m.session_id, m.created_at, m.updated_at, m.expires_at, m.access_count, m.version, m.processed_by, m.project, m.consumed_mask,
		       m.message_uuid, m.parent_uuid, m.role, m.git_branch, m.model, m.prompt_id
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
	query += " ORDER BY m.created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		// FTS failed, fall back to LIKE
		return s.searchLike(opts)
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
		return nil, nil
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

func (s *SqliteStore) searchLike(opts SearchOptions) ([]*Memory, error) {
	queryLower := strings.ToLower(opts.Query)
	memories, err := s.List(ListOptions{Phase: opts.Phase, Scope: opts.Scope})
	if err != nil {
		return nil, err
	}
	var results []*Memory
	for _, mem := range memories {
		if !strings.Contains(strings.ToLower(mem.Content), queryLower) {
			continue
		}
		results = append(results, mem)
	}
	results = s.filterSearch(results, opts)
	return results, nil
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
		       m.session_id, m.created_at, m.updated_at, m.expires_at, m.access_count, m.version, m.processed_by, m.project, m.consumed_mask,
		       m.message_uuid, m.parent_uuid, m.role, m.git_branch, m.model, m.prompt_id
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
	// content_hash is the primary key, so INSERT OR IGNORE dedups identical content idempotently.
	rawHash := mem.ContentHash
	if rawHash == "" {
		rawHash = HashContent(mem.Content)
	}
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO raw_entries (id, content, source, content_hash) VALUES (?, ?, ?, ?)`,
		rawHash, mem.Content, mem.Source, rawHash,
	); err != nil {
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
				message_uuid = CASE WHEN message_uuid = '' AND ? != '' THEN ? ELSE message_uuid END,
				parent_uuid  = CASE WHEN parent_uuid  = '' AND ? != '' THEN ? ELSE parent_uuid END,
				role         = CASE WHEN role         = '' AND ? != '' THEN ? ELSE role END,
				git_branch   = CASE WHEN git_branch   = '' AND ? != '' THEN ? ELSE git_branch END,
				model        = CASE WHEN model        = '' AND ? != '' THEN ? ELSE model END,
				prompt_id    = CASE WHEN prompt_id    = '' AND ? != '' THEN ? ELSE prompt_id END
			WHERE id = ?`,
			mem.SessionID, mem.SessionID,
			mem.Project, mem.Project,
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

	_, err = tx.Exec(`INSERT INTO memories (id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, raw_entry_id, project, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mem.ID, mem.Content, mem.ContentHash, string(mem.Phase), string(categoryVal),
		mem.Scope, mem.Source, mem.SessionID,
		mem.CreatedAt.Format(time.RFC3339), mem.UpdatedAt.Format(time.RFC3339),
		formatTime(mem.ExpiresAt), mem.AccessCount, mem.Version, string(pbData), rawHash, mem.Project, mem.ConsumedMask,
		mem.MessageUUID, mem.ParentUUID, mem.Role, mem.GitBranch, mem.Model, mem.PromptID)
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
	err := rows.Scan(&mem.ID, &mem.Content, &mem.ContentHash, &phase, &category,
		&scope, &source, &sessionID, &createdAt, &updatedAt, &expiresAt, &mem.AccessCount, &mem.Version, &processedBy, &project, &mem.ConsumedMask,
		&mem.MessageUUID, &mem.ParentUUID, &mem.Role, &mem.GitBranch, &mem.Model, &mem.PromptID)
	if err != nil {
		return nil, err
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
	return &mem, nil
}

func scanMemoryRowSingle(row *sql.Row) (*Memory, error) {
	var mem Memory
	var phase, category, scope, source, sessionID, project string
	var createdAt, updatedAt string
	var expiresAt sql.NullString
	var processedBy string
	err := row.Scan(&mem.ID, &mem.Content, &mem.ContentHash, &phase, &category,
		&scope, &source, &sessionID, &createdAt, &updatedAt, &expiresAt, &mem.AccessCount, &mem.Version, &processedBy, &project, &mem.ConsumedMask,
		&mem.MessageUUID, &mem.ParentUUID, &mem.Role, &mem.GitBranch, &mem.Model, &mem.PromptID)
	if err != nil {
		return nil, err
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
	return &mem, nil
}

func formatTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func (s *SqliteStore) IngestMemory(mem *Memory) error {
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
	return s.InsertMemory(mem)
}
