package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [id]",
	Short: "Upgrade a short-term memory to long-term",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		if err := s.Upgrade(args[0]); err != nil {
			return err
		}
		fmt.Printf("Upgraded memory %s to long-term\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}
