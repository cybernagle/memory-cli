package store

import (
	"fmt"
	"strings"
	"time"
)

// Graduation queue: work-context facts ("客户确认了 v26", "合同条款敲定") waiting to be
// archived into the project's business system of record (PocketBase, CRM, ...). The
// boundary rule: business data lives in the business system; memory keeps only a pointer
// (pb_pointer). Graduating a fact = recording it there and marking done here.

// Graduation is one queued fact.
type Graduation struct {
	ID         int64  `json:"id"`
	Project    string `json:"project"`
	Fact       string `json:"fact"`
	SessionID  string `json:"session_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	ArchivedAt string `json:"archived_at,omitempty"` // empty = pending
	PBPointer  string `json:"pb_pointer,omitempty"`
}

func (g *Graduation) Pending() bool { return g.ArchivedAt == "" }

// AddGraduation enqueues a fact for archival into the business system of record.
func (s *SqliteStore) AddGraduation(project, fact, sessionID string) (*Graduation, error) {
	project = strings.TrimSpace(project)
	fact = strings.TrimSpace(fact)
	if project == "" || fact == "" {
		return nil, fmt.Errorf("project and fact are required")
	}
	if len(fact) > 2000 {
		return nil, fmt.Errorf("fact too long (max 2000)")
	}
	g := &Graduation{Project: project, Fact: fact, SessionID: sessionID,
		CreatedAt: time.Now().Format(time.RFC3339)}
	res, err := s.db.Exec(
		"INSERT INTO graduations (project, fact, session_id, created_at) VALUES (?, ?, ?, ?)",
		project, fact, sessionID, g.CreatedAt)
	if err != nil {
		return nil, err
	}
	g.ID, _ = res.LastInsertId()
	// Keep the bootstrap file current so the queue is visible at session start.
	s.writeStateMarkdown()
	return g, nil
}

// ListGraduations returns queue entries, pending first then newest first. When
// onlyPending, archived entries are excluded.
func (s *SqliteStore) ListGraduations(onlyPending bool) ([]*Graduation, error) {
	query := "SELECT id, project, fact, session_id, created_at, COALESCE(archived_at,''), pb_pointer FROM graduations"
	if onlyPending {
		query += " WHERE archived_at IS NULL"
	}
	query += " ORDER BY (archived_at IS NULL) DESC, id DESC LIMIT 100"
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Graduation
	for rows.Next() {
		g := &Graduation{}
		if err := rows.Scan(&g.ID, &g.Project, &g.Fact, &g.SessionID, &g.CreatedAt, &g.ArchivedAt, &g.PBPointer); err == nil {
			out = append(out, g)
		}
	}
	return out, nil
}

// CompleteGraduation marks a fact archived into the business system, keeping the pointer.
func (s *SqliteStore) CompleteGraduation(id int64, pbPointer string) error {
	pbPointer = strings.TrimSpace(pbPointer)
	if pbPointer == "" {
		return fmt.Errorf("pb_pointer is required (where the fact was archived, e.g. pb://ruifulai/feedback/rec123)")
	}
	res, err := s.db.Exec(
		"UPDATE graduations SET archived_at = ?, pb_pointer = ? WHERE id = ? AND archived_at IS NULL",
		time.Now().Format(time.RFC3339), pbPointer, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: graduation %d not found or already archived", ErrNotFound, id)
	}
	// Refresh the bootstrap file so the queue section stays current.
	s.writeStateMarkdown()
	return nil
}
