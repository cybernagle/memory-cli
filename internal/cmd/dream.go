package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/daemon"
)

var dreamLevel int

var dreamCmd = &cobra.Command{
	Use:   "dream",
	Short: "Run dream engine to process and organize memories",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		level := daemon.DreamLevel(dreamLevel)
		if level < 1 || level > 3 {
			return fmt.Errorf("invalid dream level %d (must be 1-3)", level)
		}

		task := &daemon.DreamTask{Level: level}
		count, err := task.Run(s)
		if err != nil {
			return err
		}
		fmt.Printf("Dream (level %d) processed %d items\n", level, count)
		return nil
	},
}

func init() {
	dreamCmd.Flags().IntVar(&dreamLevel, "level", 1, "Dream depth: 1=light, 2=medium, 3=deep")
	rootCmd.AddCommand(dreamCmd)
}
