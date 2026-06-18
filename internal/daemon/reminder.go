package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

// ReminderTask checks the reminders table for due items and fires notifications.
// Unlike the old NotifyTask (which read category=reminders memories and re-fired them every
// tick because it never marked them as fired), this task marks each reminder 'fired' after
// notifying, so it fires exactly once.
type ReminderTask struct {
	cfg *config.Config
	db  interface {
		ListDueReminders(now time.Time) ([]*store.Reminder, error)
		MarkReminderFired(id string) error
	}
}

func NewReminderTask(cfg *config.Config) *ReminderTask {
	return &ReminderTask{cfg: cfg}
}

// SetStore links the task to a SqliteStore (for the ListDueReminders/MarkReminderFired calls).
// Called when the task is added to the daemon.
func (t *ReminderTask) SetStore(s *store.SqliteStore) {
	t.db = s
}

func (t *ReminderTask) Name() string { return "reminder" }

func (t *ReminderTask) Run(s store.Store) (int, error) {
	if t.db == nil {
		// Try to get the SqliteStore from the interface.
		if sqlStore, ok := s.(*store.SqliteStore); ok {
			t.db = sqlStore
		} else {
			return 0, nil // non-SQLite store, nothing to do
		}
	}

	now := time.Now().In(t.cfg.Location())
	due, err := t.db.ListDueReminders(now)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}

	var firedItems []string
	for _, r := range due {
		// Build the notification text. Prefix with the trigger time for context.
		timeStr := r.TriggerAt.Format("15:04")
		item := fmt.Sprintf("[%s] %s", timeStr, r.Content)
		firedItems = append(firedItems, item)

		// Mark fired BEFORE pushing the notification — if the notification fails, we still
		// don't want to re-fire it every tick (a missed notification is better than spam).
		if err := t.db.MarkReminderFired(r.ID); err != nil {
			log.Printf("reminder: mark fired %s: %v", r.ID, err)
			continue
		}
	}

	// Write pending.md — this file is what agents (Makro, zcode, Claude Code) can read on
	// startup to surface due reminders in their context.
	if err := t.writePendingFile(firedItems); err != nil {
		log.Printf("reminder: write pending.md: %v", err)
	}

	// Push macOS notification if configured.
	if t.cfg.Notification.Enabled && t.cfg.Notification.Method == "osascript" {
		t.pushNotification(firedItems)
	}

	log.Printf("[reminder] fired %d reminder(s)", len(firedItems))
	return len(firedItems), nil
}

func (t *ReminderTask) writePendingFile(items []string) error {
	// Also include still-pending (not yet due) reminders so agents see the full queue.
	pending, _ := t.db.ListDueReminders(time.Now().Add(365 * 24 * time.Hour)) // all pending up to a year
	allItems := make(map[string]bool)
	for _, item := range items {
		allItems[item] = true
	}
	for _, r := range pending {
		timeStr := r.TriggerAt.Format("01-02 15:04")
		allItems[fmt.Sprintf("[%s] %s", timeStr, r.Content)] = true
	}

	var sb strings.Builder
	sb.WriteString("# Pending Reminders\n\n")
	idx := 0
	for item := range allItems {
		idx++
		sb.WriteString(fmt.Sprintf("%d. %s\n", idx, item))
	}
	sb.WriteString("\n")

	tmpPath := t.cfg.PendingPath() + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(sb.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, t.cfg.PendingPath())
}

func (t *ReminderTask) pushNotification(items []string) {
	title := "🔔 Memory Reminder"
	var message string
	if len(items) == 1 {
		message = items[0]
		if len(message) > 120 {
			message = message[:120] + "..."
		}
	} else {
		message = fmt.Sprintf("你有 %d 个提醒待处理", len(items))
	}

	script := fmt.Sprintf(`display notification %q with title %q sound name "Glass"`, message, title)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		log.Printf("reminder: osascript failed: %v", err)
	}
}
