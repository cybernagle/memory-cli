package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/daemon"
)

var consolidateCmd = &cobra.Command{
	Use:   "consolidate",
	Short: "Run daemon tasks once (expire, decay, upgrade, dedup)",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		results := daemon.RunOnce(s)
		for name, count := range results {
			if count >= 0 {
				fmt.Printf("%-15s %d items processed\n", name, count)
			} else {
				fmt.Printf("%-15s error\n", name)
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(consolidateCmd)
}
