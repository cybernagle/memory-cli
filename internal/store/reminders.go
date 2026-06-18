package store

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Reminder is an event-driven trigger: a piece of content ("去查X") with a trigger timestamp
// and a lifecycle (pending → fired → done). Unlike memories (permanent facts), reminders are
// temporary tasks that fire a notification when trigger_at is reached, then get marked fired.
type Reminder struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	TriggerAt time.Time `json:"trigger_at"`
	Status    string    `json:"status"` // pending / fired / done / cancelled
	CreatedAt time.Time `json:"created_at"`
	FiredAt   *time.Time `json:"fired_at,omitempty"`
	Source    string    `json:"source"`
}

// CreateReminder stores a new pending reminder.
func (s *SqliteStore) CreateReminder(r *Reminder) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	if r.Source == "" {
		r.Source = "cli"
	}
	_, err := s.db.Exec(`INSERT INTO reminders (id, content, trigger_at, status, created_at, source)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Content, r.TriggerAt.Format(time.RFC3339), r.Status, r.CreatedAt.Format(time.RFC3339), r.Source)
	return err
}

// ListReminders returns reminders matching the status filter. If status is empty, returns all.
func (s *SqliteStore) ListReminders(status string) ([]*Reminder, error) {
	query := "SELECT id, content, trigger_at, status, created_at, fired_at, source FROM reminders"
	var args []interface{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY trigger_at ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []*Reminder
	for rows.Next() {
		var r Reminder
		var triggerAt, createdAt string
		var firedAt *string
		if err := rows.Scan(&r.ID, &r.Content, &triggerAt, &r.Status, &createdAt, &firedAt, &r.Source); err != nil {
			continue
		}
		r.TriggerAt, _ = time.Parse(time.RFC3339, triggerAt)
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if firedAt != nil && *firedAt != "" {
			t, err := time.Parse(time.RFC3339, *firedAt)
			if err == nil {
				r.FiredAt = &t
			}
		}
		reminders = append(reminders, &r)
	}
	return reminders, nil
}

// ListDueReminders returns pending reminders whose trigger_at has passed (<= now).
func (s *SqliteStore) ListDueReminders(now time.Time) ([]*Reminder, error) {
	rows, err := s.db.Query(`SELECT id, content, trigger_at, status, created_at, fired_at, source
		FROM reminders WHERE status = 'pending' AND trigger_at <= ? ORDER BY trigger_at ASC`,
		now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []*Reminder
	for rows.Next() {
		var r Reminder
		var triggerAt, createdAt string
		var firedAt *string
		if err := rows.Scan(&r.ID, &r.Content, &triggerAt, &r.Status, &createdAt, &firedAt, &r.Source); err != nil {
			continue
		}
		r.TriggerAt, _ = time.Parse(time.RFC3339, triggerAt)
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if firedAt != nil && *firedAt != "" {
			t, err := time.Parse(time.RFC3339, *firedAt)
			if err == nil {
				r.FiredAt = &t
			}
		}
		reminders = append(reminders, &r)
	}
	return reminders, nil
}

// MarkReminderFired sets a reminder's status to fired and records the fire time.
// Called after the notification is sent so it doesn't re-fire on the next tick.
func (s *SqliteStore) MarkReminderFired(id string) error {
	_, err := s.db.Exec("UPDATE reminders SET status = 'fired', fired_at = ? WHERE id = ?",
		time.Now().Format(time.RFC3339), id)
	return err
}

// MarkReminderStatus sets a reminder to an arbitrary status (done/cancelled/fired).
// Accepts a short ID prefix (first 8+ chars) for convenience — the CLI displays truncated IDs.
func (s *SqliteStore) MarkReminderStatus(idPrefix, status string) error {
	// If it's a full UUID, match directly. Otherwise treat as prefix and match LIKE 'prefix%'.
	if len(idPrefix) >= 32 {
		_, err := s.db.Exec("UPDATE reminders SET status = ? WHERE id = ?", status, idPrefix)
		return err
	}
	res, err := s.db.Exec("UPDATE reminders SET status = ? WHERE id LIKE ?", status, idPrefix+"%")
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no reminder matching id prefix %q", idPrefix)
	}
	return nil
}
