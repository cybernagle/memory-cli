package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/daemon"
)

var decayCmd = &cobra.Command{
	Use:   "decay",
	Short: "Remove unused long-term memories (>30 days, never accessed)",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		task := &daemon.DecayTask{}
		count, err := task.Run(s)
		if err != nil {
			return err
		}
		fmt.Printf("Decayed %d unused memories\n", count)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(decayCmd)
}
