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

// NotifyTask checks for reminders and pushes notifications.
type NotifyTask struct {
	cfg *config.Config
}

func NewNotifyTask(cfg *config.Config) *NotifyTask {
	return &NotifyTask{cfg: cfg}
}

func (t *NotifyTask) Name() string { return "notify" }

func (t *NotifyTask) Run(s *store.Store) (int, error) {
	if !t.cfg.Notification.Enabled {
		return 0, nil
	}

	reminders, err := s.List(store.ListOptions{Category: store.CategoryReminders})
	if err != nil {
		return 0, err
	}
	if len(reminders) == 0 {
		return 0, nil
	}

	now := time.Now()
	loc := t.cfg.Location()
	count := 0

	var pendingItems []string

	for _, mem := range reminders {
		// Extract trigger time from content if present
		triggerTime := extractTriggerTime(mem.Content, loc)
		if triggerTime == nil || now.After(*triggerTime) {
			pendingItems = append(pendingItems, mem.Content)
			count++
		}
	}

	if count > 0 {
		// Write to pending.md for agents to consume on startup
		if err := writePendingFile(t.cfg, pendingItems); err != nil {
			log.Printf("notify: write pending.md failed: %v", err)
		}

		// Push macOS notification
		if t.cfg.Notification.Method == "osascript" {
			pushNotification(pendingItems)
		}
	}

	return count, nil
}

func writePendingFile(cfg *config.Config, items []string) error {
	var sb strings.Builder
	sb.WriteString("# Pending Reminders\n\n")
	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, item))
	}
	sb.WriteString("\n")

	tmpPath := cfg.PendingPath() + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(sb.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, cfg.PendingPath())
}

func pushNotification(items []string) {
	title := "Memory CLI"
	message := fmt.Sprintf("You have %d reminder(s)", len(items))
	if len(items) == 1 {
		preview := items[0]
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		message = preview
	}

	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		log.Printf("notify: osascript failed: %v", err)
	}
}

// extractTriggerTime looks for time patterns in reminder content.
// Supports: YYYY-MM-DD HH:MM, tomorrow, 下周, etc.
func extractTriggerTime(content string, loc *time.Location) *time.Time {
	now := time.Now().In(loc)
	lower := strings.ToLower(content)

	// Pattern: [[HH:MM]] or @HH:MM
	for _, prefix := range []string{"[[", "@", "at "} {
		if idx := strings.Index(lower, prefix); idx != -1 {
			rest := lower[idx+len(prefix):]
			if len(rest) >= 5 && rest[2] == ':' {
				var h, m int
				if _, err := fmt.Sscanf(rest[:5], "%d:%d", &h, &m); err == nil {
					t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
					if t.Before(now) {
						t = t.Add(24 * time.Hour)
					}
					return &t
				}
			}
		}
	}

	// Pattern: YYYY-MM-DD
	for _, prefix := range []string{"deadline:", "截止:", "due:", "by "} {
		if idx := strings.Index(lower, prefix); idx != -1 {
			dateStr := strings.TrimSpace(lower[idx+len(prefix):])
			if t, err := time.ParseInLocation("2006-01-02", dateStr[:min(len(dateStr), 10)], loc); err == nil {
				return &t
			}
		}
	}

	// Default: the ExpiresAt on the memory itself
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
