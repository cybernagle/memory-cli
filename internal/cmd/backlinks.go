package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var backlinksCmd = &cobra.Command{
	Use:   "backlinks <id>",
	Short: "Show all memories that link to the given memory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		backlinks, err := s.GetBacklinks(args[0])
		if err != nil {
			return err
		}
		if len(backlinks) == 0 {
			fmt.Println("No backlinks found.")
			return nil
		}
		fmt.Printf("%-8s %-10s %-10s %s\n", "ID", "Phase", "Category", "Preview")
		fmt.Println(strings.Repeat("-", 80))
		for _, m := range backlinks {
			preview := truncateRunes(m.Content, 50)
			preview = strings.ReplaceAll(preview, "\n", " ")
			fmt.Printf("%-8s %-10s %-10s %s\n", m.ID[:8], m.Phase, m.Category, preview)
		}
		fmt.Printf("\nFound: %d backlinks\n", len(backlinks))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(backlinksCmd)
}
