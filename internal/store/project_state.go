package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Project-state sharing: the shared working memory between agents. Precision comes from
// program-written reports (agent → memory_state_set), never from LLM extraction. The
// current state is a latest-value projection (project_states); every change is also
// appended to project_state_history (audit/handoff trail). Git stays the authority for
// code — we store only pointers (branch/commit) plus what git cannot express.

// StaleAfter is how old a state may get before readers must verify against git before
// trusting it. Handoff data, not gospel.
const StaleAfter = 24 * time.Hour

// ProjectState is one project's shared working state.
type ProjectState struct {
	Project     string   `json:"project"`
	Version     string   `json:"version,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Commit      string   `json:"commit,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	Blockers    []string `json:"blockers"`
	NextActions []string `json:"next_actions"`
	Notes       string   `json:"notes,omitempty"`
	UpdatedAt   string   `json:"updated_at"`
	UpdatedBy   string   `json:"updated_by,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`

	// Computed at read time, not stored.
	Stale    bool    `json:"stale"`
	AgeHours float64 `json:"age_hours"`
}

// StateInput is what a writer reports. UpdatedAt is always server-side now().
type StateInput struct {
	Project     string
	Version     string
	Branch      string
	Commit      string
	Phase       string
	Blockers    []string
	NextActions []string
	Notes       string
	UpdatedBy   string
	SessionID   string
}

// SetProjectState validates, upserts the current-state projection, appends the history
// log and refreshes the state.md bootstrap file. Returns the stored state.
func (s *SqliteStore) SetProjectState(in StateInput) (*ProjectState, error) {
	in.Project = strings.TrimSpace(in.Project)
	if in.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	for field, v := range map[string]string{
		"version": in.Version, "branch": in.Branch, "commit": in.Commit, "phase": in.Phase,
	} {
		if len(v) > 128 {
			return nil, fmt.Errorf("%s too long (max 128)", field)
		}
	}
	if len(in.Notes) > 8*1024 {
		return nil, fmt.Errorf("notes too long (max 8KB)")
	}
	in.Notes = strings.TrimSpace(in.Notes)

	now := time.Now().Format(time.RFC3339)
	blockers, err := json.Marshal(normalizeTags(in.Blockers))
	if err != nil {
		return nil, err
	}
	next, err := json.Marshal(normalizeTags(in.NextActions))
	if err != nil {
		return nil, err
	}

	stored := &ProjectState{
		Project: in.Project, Version: in.Version, Branch: in.Branch, Commit: in.Commit,
		Phase: in.Phase, Blockers: normalizeTags(in.Blockers), NextActions: normalizeTags(in.NextActions),
		Notes: in.Notes, UpdatedAt: now, UpdatedBy: in.UpdatedBy, SessionID: in.SessionID,
	}
	stateJSON, _ := json.Marshal(stored)

	if _, err := s.db.Exec(`INSERT INTO project_states
		(project, version, branch, commit_hash, phase, blockers, next_actions, notes, updated_at, updated_by, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			version = excluded.version, branch = excluded.branch, commit_hash = excluded.commit_hash,
			phase = excluded.phase, blockers = excluded.blockers, next_actions = excluded.next_actions,
			notes = excluded.notes, updated_at = excluded.updated_at, updated_by = excluded.updated_by,
			session_id = excluded.session_id`,
		in.Project, in.Version, in.Branch, in.Commit, in.Phase,
		string(blockers), string(next), in.Notes, now, in.UpdatedBy, in.SessionID); err != nil {
		return nil, err
	}
	s.db.Exec("INSERT INTO project_state_history (project, state, changed_at, changed_by) VALUES (?, ?, ?, ?)",
		in.Project, string(stateJSON), now, in.UpdatedBy)
	s.LogActivity("state-set", "", in.UpdatedBy, in.Project+" "+in.Version)

	if err := s.writeStateMarkdown(); err != nil {
		// Bootstrap file is best-effort; the DB write above already succeeded.
		fmt.Fprintf(os.Stderr, "[state] state.md refresh failed: %v\n", err)
	}
	return stored, nil
}

// GetProjectState returns one project's state with staleness computed at read time.
func (s *SqliteStore) GetProjectState(project string) (*ProjectState, error) {
	rows, err := s.db.Query(`SELECT project, version, branch, commit_hash, phase, blockers,
		next_actions, notes, updated_at, updated_by, session_id
		FROM project_states WHERE project = ?`, strings.TrimSpace(project))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("%w: no state for project %q", ErrNotFound, project)
	}
	return scanProjectState(rows)
}

// ListProjectStates returns all states, freshest first.
func (s *SqliteStore) ListProjectStates() ([]*ProjectState, error) {
	rows, err := s.db.Query(`SELECT project, version, branch, commit_hash, phase, blockers,
		next_actions, notes, updated_at, updated_by, session_id
		FROM project_states ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectState
	for rows.Next() {
		ps, err := scanProjectState(rows)
		if err != nil {
			continue
		}
		out = append(out, ps)
	}
	return out, nil
}

// StateHistory returns a project's state-change trail, newest first.
func (s *SqliteStore) StateHistory(project string, limit int) ([]*ProjectState, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT state FROM project_state_history
		WHERE project = ? ORDER BY seq DESC LIMIT ?`, strings.TrimSpace(project), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectState
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var ps ProjectState
		if json.Unmarshal([]byte(raw), &ps) == nil {
			out = append(out, &ps)
		}
	}
	return out, nil
}

func scanProjectState(rows *sql.Rows) (*ProjectState, error) {
	ps := &ProjectState{}
	var blockersJSON, nextJSON string
	if err := rows.Scan(&ps.Project, &ps.Version, &ps.Branch, &ps.Commit, &ps.Phase,
		&blockersJSON, &nextJSON, &ps.Notes, &ps.UpdatedAt, &ps.UpdatedBy, &ps.SessionID); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(blockersJSON), &ps.Blockers)
	json.Unmarshal([]byte(nextJSON), &ps.NextActions)
	if ps.Blockers == nil {
		ps.Blockers = []string{}
	}
	if ps.NextActions == nil {
		ps.NextActions = []string{}
	}
	if t, err := time.Parse(time.RFC3339, ps.UpdatedAt); err == nil {
		ps.AgeHours = time.Since(t).Hours()
		ps.Stale = ps.AgeHours > StaleAfter.Hours()
	}
	return ps, nil
}

// writeStateMarkdown regenerates the bootstrap file next to the DB (state.md): one block
// per project so any agent can read the whole board in one file read at session start
// (same pattern as pending.md for reminders).
func (s *SqliteStore) writeStateMarkdown() error {
	states, err := s.ListProjectStates()
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("# Project States · 共享工作状态\n\n")
	sb.WriteString("> 由 agent 在收尾/里程碑时写入(memory_state_set)。STALE 条目必须先 git 核实再动工。\n\n")
	for _, ps := range states {
		flag := ""
		if ps.Stale {
			flag = " ⚠️STALE"
		}
		sb.WriteString(fmt.Sprintf("## %s — %s @ %s(%s) · %s · 更新 %s by %s%s\n",
			ps.Project, defaultString(ps.Version, "-"), defaultString(ps.Branch, "-"),
			ps.CommitShort(), defaultString(ps.Phase, "-"), ps.UpdatedAt[:min(16, len(ps.UpdatedAt))],
			defaultString(ps.UpdatedBy, "?"), flag))
		if len(ps.Blockers) > 0 {
			sb.WriteString("- blockers: " + strings.Join(ps.Blockers, " / ") + "\n")
		}
		if len(ps.NextActions) > 0 {
			sb.WriteString("- next: " + strings.Join(ps.NextActions, " / ") + "\n")
		}
		if ps.Notes != "" {
			sb.WriteString("- notes: " + ps.Notes + "\n")
		}
		sb.WriteString("\n")
	}
	path := s.stateMarkdownPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

// stateMarkdownPath places state.md next to the SQLite DB (~/.memory/state.md for the
// default layout) — the store has no config dependency, so the DB location defines home.
func (s *SqliteStore) stateMarkdownPath() string {
	if s.dbPath != "" {
		return filepath.Join(filepath.Dir(s.dbPath), "state.md")
	}
	return "state.md"
}

// CommitShort renders the commit pointer for humans (7 chars).
func (ps *ProjectState) CommitShort() string {
	if len(ps.Commit) > 7 {
		return ps.Commit[:7]
	}
	return ps.Commit
}
