package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Check reminders and push notifications",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		task := daemon.NewNotifyTask(cfg)
		count, err := task.Run(s)
		if err != nil {
			return err
		}
		fmt.Printf("Notified %d reminders\n", count)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(notifyCmd)
}
