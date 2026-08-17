package store

import (
	"fmt"
	"strings"
	"time"
)

// Rebuild: recreate derived views from the append-only event log (raw_entries).
//
// DDIA ch3 (event sourcing / CQRS): the event log is the source of truth; every derived
// structure (memories rows, tags, links, the FTS index, the entity graph) must be
// reproducible from it. Two modes:
//
//   - RebuildIndexes  (safe): rebuild only the deterministic derived structures — the FTS
//     index, wiki-links, keyword tags — without touching memory rows. Use when an index is
//     corrupted or a tokenizer migration went wrong.
//
//   - RebuildFromEvents (full): wipe the entire derived layer and replay the event log in
//     sequence order. Content and provenance are restored exactly; processing state is NOT
//     — replayed memories land in inbox with consumed_mask=0 so the daemon pipelines
//     (consolidate / entity / profile) re-derive them. Because those pipelines are LLM-based
//     (non-deterministic), re-derived results may differ from the originals. That is the
//     known cost of a non-deterministic projector (DDIA ch3, "deterministic view
//     processing"), accepted deliberately.

// RawEntry is one event from the append-only log.
type RawEntry struct {
	ID      string
	Content string
	Source  string
	// IngestedAt is the raw SQLite datetime string ("2006-01-02 15:04:05", UTC).
	IngestedAt string

	SessionID   string
	Project     string
	TmuxSession string
	MessageUUID string
	ParentUUID  string
	Role        string
	GitBranch   string
	Model       string
	PromptID    string
}

// ListRawEntries streams the event log in append order. rowid is the monotonic event
// sequence (insert order), which is what replay must follow.
func (s *SqliteStore) ListRawEntries() ([]RawEntry, error) {
	rows, err := s.db.Query(`SELECT id, content, source, ingested_at,
		session_id, project, tmux_session, message_uuid, parent_uuid, role, git_branch, model, prompt_id
		FROM raw_entries ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawEntry
	for rows.Next() {
		var e RawEntry
		if err := rows.Scan(&e.ID, &e.Content, &e.Source, &e.IngestedAt,
			&e.SessionID, &e.Project, &e.TmuxSession, &e.MessageUUID, &e.ParentUUID,
			&e.Role, &e.GitBranch, &e.Model, &e.PromptID); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// RebuildStats reports what a rebuild did.
type RebuildStats struct {
	Mode       string // "indexes" | "full"
	FTSRows    int    // rows reindexed into memories_fts
	Links      int    // wiki-links (re)created
	TagsAdded  int    // keyword tags added
	Events     int    // events in the log (full mode)
	Rebuilt    int    // memories recreated from events (full mode)
	Skipped    int    // events rejected by the command gate (full mode)
	DurationMs int64
}

// rebuildFTS drops and recreates memories_fts with the trigram tokenizer, then backfills
// from memories + tags. Shared by the unicode61 migration and both rebuild modes.
func (s *SqliteStore) rebuildFTS() (int, error) {
	s.db.Exec("DROP TABLE IF EXISTS memories_fts")
	if _, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
		memory_id UNINDEXED,
		content,
		tags,
		scope,
		source,
		tokenize='trigram'
	)`); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO memories_fts (memory_id, content, tags, scope, source)
		SELECT m.id, m.content,
		       COALESCE((SELECT group_concat(t.tag, ' ') FROM tags t WHERE t.memory_id = m.id), ''),
		       m.scope, m.source
		FROM memories m`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RebuildIndexes rebuilds the deterministic derived structures (FTS, links, keyword tags)
// without touching memory rows. Safe to run at any time.
func (s *SqliteStore) RebuildIndexes() (*RebuildStats, error) {
	start := time.Now()
	stats := &RebuildStats{Mode: "indexes"}

	ftsRows, err := s.rebuildFTS()
	if err != nil {
		return nil, fmt.Errorf("rebuild fts: %w", err)
	}
	stats.FTSRows = ftsRows

	// Links: wipe and re-derive from wikilinks in content (fully deterministic).
	s.db.Exec("DELETE FROM links")
	links, err := s.ResolveBacklinks()
	if err != nil {
		return nil, fmt.Errorf("resolve backlinks: %w", err)
	}
	stats.Links = links

	// Keyword tag enrichment: re-run the free-layer extractor per memory. INSERT OR IGNORE
	// makes this idempotent — only tags an older code version missed get added.
	memories, err := s.List(ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	for _, mem := range memories {
		for _, tag := range ExtractKeywords(mem.Content) {
			res, err := s.db.Exec("INSERT OR IGNORE INTO tags (memory_id, tag) VALUES (?, ?)", mem.ID, tag)
			if err != nil {
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				stats.TagsAdded++
			}
		}
	}

	stats.DurationMs = time.Since(start).Milliseconds()
	return stats, nil
}

// RebuildFromEvents wipes the derived layer and replays the event log. DESTRUCTIVE to
// processing state: phase/LLM refinements are re-derived by the daemon afterwards.
func (s *SqliteStore) RebuildFromEvents() (*RebuildStats, error) {
	start := time.Now()
	stats := &RebuildStats{Mode: "full"}

	events, err := s.ListRawEntries()
	if err != nil {
		return nil, fmt.Errorf("read event log: %w", err)
	}
	stats.Events = len(events)
	if len(events) == 0 {
		return nil, fmt.Errorf("event log is empty — refusing to wipe the derived layer")
	}

	// Wipe derived tables. Entities live in the same DB (entity store); wipe if present.
	// reminders and activity_log are NOT derived from the event log — untouched.
	for _, table := range []string{"memories", "tags", "links", "memories_fts", "entity_mentions", "entities"} {
		var exists bool
		s.db.QueryRow(
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists)
		if exists {
			s.db.Exec("DELETE FROM " + table)
		}
	}

	// Replay in sequence order through the unified write path: the command gate, event
	// append, auto-categorization and dedup all apply exactly as on a live write.
	// Historical noise events (pre-filter thinking ingestion, system notifications) stay
	// in the log as honest history but never re-enter the derived layer.
	for _, e := range events {
		if isNoiseEvent(e.Content) {
			stats.Skipped++
			continue
		}
		createdAt, _ := time.Parse("2006-01-02 15:04:05", e.IngestedAt)
		mem := &Memory{
			Content:     e.Content,
			Phase:       PhaseInbox,
			Category:    CategoryInbox, // auto-categorized at the insert chokepoint
			Source:      defaultString(e.Source, "manual"),
			SessionID:   e.SessionID,
			Project:     e.Project,
			TmuxSession: e.TmuxSession,
			MessageUUID: e.MessageUUID,
			ParentUUID:  e.ParentUUID,
			Role:        e.Role,
			GitBranch:   e.GitBranch,
			Model:       e.Model,
			PromptID:    e.PromptID,
			CreatedAt:   createdAt,
			Links:       ExtractWikiLinks(e.Content),
		}
		if err := s.IngestMemory(mem); err != nil {
			stats.Skipped++ // gate rejected (e.g. event content became invalid under new rules)
			continue
		}
		stats.Rebuilt++
	}

	links, err := s.ResolveBacklinks()
	if err != nil {
		return stats, fmt.Errorf("resolve backlinks after replay: %w", err)
	}
	stats.Links = links

	stats.DurationMs = time.Since(start).Milliseconds()
	return stats, nil
}

// Describe prints a one-line summary for CLI output.
func (st *RebuildStats) Describe() string {
	if st.Mode == "full" {
		return fmt.Sprintf("replayed %d/%d events (skipped %d), %d links, %dms",
			st.Rebuilt, st.Events, st.Skipped, st.Links, st.DurationMs)
	}
	return fmt.Sprintf("reindexed %d rows into FTS, %d links, +%d tags, %dms",
		st.FTSRows, st.Links, st.TagsAdded, st.DurationMs)
}


// isNoiseEvent reports whether a historical event is process noise rather than work
// signal: model thinking blocks (ingested before the 2026-08-17 filter) and injected
// system notifications. These remain in raw_entries as history but are excluded from
// rebuilt views.
func isNoiseEvent(content string) bool {
	c := strings.TrimSpace(content)
	if strings.HasPrefix(c, "[thinking]") {
		return true
	}
	// Task/system notifications injected as pseudo user-turns ("Q: <task-notification>...").
	if strings.HasPrefix(c, "Q: <task-notification") || strings.HasPrefix(c, "<task-notification") {
		return true
	}
	return false
}
