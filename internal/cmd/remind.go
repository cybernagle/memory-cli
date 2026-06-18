package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/store"
)

var remindAt string

// remindCmd creates a time-based reminder. The time can be embedded in the content
// ("下午3点去查X") or specified separately via --at. The content is what gets shown in the
// notification when the reminder fires.
var remindCmd = &cobra.Command{
	Use:   `remind "<what to do>"`,
	Short: "Create a time-based reminder (e.g. memory remind \"下午3点查GLM定价\")",
	Long: `Create a time-based reminder.

The trigger time is parsed from natural language (Chinese or English). It can be
embedded in the content or given via --at.

Examples:
  memory remind "下午3点去查GLM的定价"
  memory remind "跟进makro的PR" --at "明天上午10点"
  memory remind "检查deploy状态" --at "2小时后"
  memory remind "下周一开周会"
  memory remind "test reminder" --at "in 5 minutes"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRemind,
}

func init() {
	remindCmd.Flags().StringVar(&remindAt, "at", "", "Trigger time (parsed separately from content)")
	rootCmd.AddCommand(remindCmd)
}

func runRemind(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	s, err := store.NewSqliteStoreFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	loc := cfg.Location()
	content := args[0]
	if len(args) > 1 {
		// If multiple args, join them (cobra passes quoted string as one arg, but be safe).
		content = ""
		for _, a := range args {
			if content != "" {
				content += " "
			}
			content += a
		}
	}

	var triggerAt time.Time
	if remindAt != "" {
		// Time specified separately via --at. Content stays as-is.
		triggerAt, _ = daemon.ParseReminderTime(remindAt, loc)
	} else {
		// Time embedded in content — parse it out, keep the remainder as the content.
		var remainder string
		triggerAt, remainder = daemon.ParseReminderTime(content, loc)
		if remainder != "" && remainder != content {
			content = remainder
		}
	}

	reminder := &store.Reminder{
		Content:   content,
		TriggerAt: triggerAt,
		Source:    "cli",
	}
	if err := s.CreateReminder(reminder); err != nil {
		return fmt.Errorf("create reminder: %w", err)
	}

	fmt.Printf("✓ 提醒已创建\n")
	fmt.Printf("  内容: %s\n", content)
	fmt.Printf("  触发: %s (%s)\n", triggerAt.Format("2006-01-02 15:04"), humanizeDuration(time.Until(triggerAt)))
	fmt.Printf("  ID:   %s\n", reminder.ID)
	return nil
}

func humanizeDuration(d time.Duration) string {
	if d < 0 {
		return "已过期"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d秒后", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分钟后", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d小时后", int(d.Hours()))
	}
	return fmt.Sprintf("%d天后", int(d.Hours()/24))
}
