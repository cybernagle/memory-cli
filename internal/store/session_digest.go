package store

import (
	"database/sql"
	"time"
)

// Session-view store methods: the per-session projection (session_views table) built by
// the daemon's SessionDigestTask. Reads are grouped by session_id; a session needs
// (re)digesting when it has memories whose consumed_mask lacks the session-digest bit.

// SessionRef identifies a session with unconsumed memories, pending digest.
type SessionRef struct {
	SessionID       string
	Project         string
	TmuxSession     string
	UnconsumedCount int
}

// SessionsWithUnconsumed returns sessions (oldest activity first) that have at least one
// memory not yet consumed by the named processor.
func (s *SqliteStore) SessionsWithUnconsumed(processorName string, limit int) ([]SessionRef, error) {
	c, ok := ConsumerByName(processorName)
	if !ok {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT session_id,
		       COALESCE(MAX(project), ''),
		       COALESCE(MAX(tmux_session), ''),
		       COUNT(*)
		FROM memories
		WHERE session_id != '' AND (consumed_mask & ?) = 0
		GROUP BY session_id
		ORDER BY MIN(created_at) ASC
		LIMIT ?`, int64(c), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionRef
	for rows.Next() {
		var r SessionRef
		if err := rows.Scan(&r.SessionID, &r.Project, &r.TmuxSession, &r.UnconsumedCount); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ListMemoriesBySession returns a session's memories oldest-first (digest prompt order).
// When limit <= 0 all memories are returned; otherwise the NEWEST limit are kept — a
// digest cares most about where the work ended up. (SQLite LIMIT -1 = unlimited.)
func (s *SqliteStore) ListMemoriesBySession(sessionID string, limit int) ([]*Memory, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.Query(`
		SELECT id, content, content_hash, phase, category, scope, source, session_id, created_at, updated_at, expires_at, access_count, version, processed_by, project, tmux_session, consumed_mask, message_uuid, parent_uuid, role, git_branch, model, prompt_id, raw_entry_id, metadata
		FROM memories WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var newestFirst []*Memory
	for rows.Next() {
		mem, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		newestFirst = append(newestFirst, mem)
	}
	// Reverse to oldest-first for prompt assembly.
	out := make([]*Memory, len(newestFirst))
	for i, m := range newestFirst {
		out[len(newestFirst)-1-i] = m
	}
	return out, nil
}

// SessionView is one row of the session_views projection.
type SessionView struct {
	SessionID   string `json:"session_id"`
	Project     string `json:"project"`
	TmuxSession string `json:"tmux_session,omitempty"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	MemoryCount int    `json:"memory_count"`
	Task        string `json:"task"`
	Entity      string `json:"entity,omitempty"`
	Facet       string `json:"facet,omitempty"`
	Summary     string `json:"summary"`
	Lessons     string `json:"lessons"` // JSON array, stored verbatim
	Model       string `json:"model,omitempty"`
	UpdatedAt   string `json:"updated_at"`
}

// UpsertSessionView inserts or refreshes one projection row (digests are re-run when a
// session gains new memories, so the whole row is replaced).
func (s *SqliteStore) UpsertSessionView(v *SessionView) error {
	if v.UpdatedAt == "" {
		v.UpdatedAt = time.Now().Format(time.RFC3339)
	}
	if v.Lessons == "" {
		v.Lessons = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO session_views
		(session_id, project, tmux_session, first_seen, last_seen, memory_count,
		 task, entity, facet, summary, lessons, model, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			project = excluded.project, tmux_session = excluded.tmux_session,
			first_seen = excluded.first_seen, last_seen = excluded.last_seen,
			memory_count = excluded.memory_count, task = excluded.task,
			entity = excluded.entity, facet = excluded.facet, summary = excluded.summary,
			lessons = excluded.lessons, model = excluded.model, updated_at = excluded.updated_at`,
		v.SessionID, v.Project, v.TmuxSession, v.FirstSeen, v.LastSeen, v.MemoryCount,
		v.Task, v.Entity, v.Facet, v.Summary, v.Lessons, v.Model, v.UpdatedAt)
	return err
}

// SessionViewFilter selects projection rows; empty fields match all.
type SessionViewFilter struct {
	SessionID string
	Project   string
	Entity    string
	Limit     int
}

// ListSessionViews returns projection rows, newest activity first.
func (s *SqliteStore) ListSessionViews(f SessionViewFilter) ([]*SessionView, error) {
	query := `SELECT session_id, project, tmux_session, first_seen, last_seen, memory_count,
		task, entity, facet, summary, lessons, model, updated_at FROM session_views WHERE 1=1`
	var args []any
	if f.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, f.SessionID)
	}
	if f.Project != "" {
		query += " AND project = ?"
		args = append(args, f.Project)
	}
	if f.Entity != "" {
		// LIKE match: entity values are LLM-freeform ("瑞福莱", "juli (memory-cli)").
		query += " AND entity LIKE ?"
		args = append(args, "%"+f.Entity+"%")
	}
	query += " ORDER BY last_seen DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*SessionView
	for rows.Next() {
		v := &SessionView{}
		var tmux sql.NullString
		if err := rows.Scan(&v.SessionID, &v.Project, &tmux, &v.FirstSeen, &v.LastSeen,
			&v.MemoryCount, &v.Task, &v.Entity, &v.Facet, &v.Summary, &v.Lessons,
			&v.Model, &v.UpdatedAt); err != nil {
			continue
		}
		if tmux.Valid {
			v.TmuxSession = tmux.String
		}
		out = append(out, v)
	}
	return out, nil
}

// SessionViewCount returns the total number of digested sessions (progress metric).
func (s *SqliteStore) SessionViewCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM session_views").Scan(&n)
	return n, err
}
