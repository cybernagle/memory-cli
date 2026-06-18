package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	remindersAll   bool
	remindersDone  string
	remindersCancel string
)

// remindersCmd lists and manages reminders.
var remindersCmd = &cobra.Command{
	Use:   "reminders",
	Short: "List, complete, or cancel reminders",
	Long: `List and manage time-based reminders.

Examples:
  memory reminders                 # list pending reminders
  memory reminders --all           # list all (including fired/done/cancelled)
  memory reminders --done <id>     # mark a reminder as done
  memory reminders --cancel <id>   # cancel a reminder`,
	RunE: runReminders,
}

func init() {
	remindersCmd.Flags().BoolVar(&remindersAll, "all", false, "Show all reminders (not just pending)")
	remindersCmd.Flags().StringVar(&remindersDone, "done", "", "Mark reminder <id> as done")
	remindersCmd.Flags().StringVar(&remindersCancel, "cancel", "", "Cancel reminder <id>")
	rootCmd.AddCommand(remindersCmd)
}

func runReminders(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	s, err := store.NewSqliteStoreFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	// --done <id>
	if remindersDone != "" {
		if err := s.MarkReminderStatus(remindersDone, "done"); err != nil {
			return err
		}
		fmt.Printf("✓ Reminder %s marked done\n", remindersDone)
		return nil
	}

	// --cancel <id>
	if remindersCancel != "" {
		if err := s.MarkReminderStatus(remindersCancel, "cancelled"); err != nil {
			return err
		}
		fmt.Printf("✓ Reminder %s cancelled\n", remindersCancel)
		return nil
	}

	// List
	status := "pending"
	if remindersAll {
		status = ""
	}
	reminders, err := s.ListReminders(status)
	if err != nil {
		return fmt.Errorf("list reminders: %w", err)
	}

	if len(reminders) == 0 {
		if remindersAll {
			fmt.Println("No reminders found.")
		} else {
			fmt.Println("No pending reminders. ✨")
		}
		return nil
	}

	now := time.Now()
	fmt.Printf("%-20s  %-10s  %-12s  %s\n", "TRIGGER", "STATUS", "ID", "CONTENT")
	fmt.Println(repeatStr("-", 78))
	for _, r := range reminders {
		triggerStr := r.TriggerAt.Format("2006-01-02 15:04")
		// For pending items, show how soon (or overdue).
		if r.Status == "pending" {
			d := r.TriggerAt.Sub(now)
			if d < 0 {
				triggerStr += " (overdue)"
			} else {
				triggerStr += " (" + humanizeDuration(d) + ")"
			}
		}
		content := r.Content
		if len(content) > 40 {
			content = content[:37] + "..."
		}
		id := r.ID
		if len(id) > 10 {
			id = id[:8] + "…"
		}
		fmt.Printf("%-20s  %-10s  %-12s  %s\n", triggerStr, r.Status, id, content)
	}
	return nil
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

// Ensure os import is used (for future stderr writes). Currently fmt covers output.
var _ = os.Stderr
