package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var readCmd = &cobra.Command{
	Use:   "read [id]",
	Short: "Read a memory by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		mem, err := s.Read(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("ID:     %s\n", mem.ID)
		fmt.Printf("Type:   %s\n", mem.Type)
		fmt.Printf("Scope:  %s\n", mem.Scope)
		fmt.Printf("Source: %s\n", mem.Source)
		fmt.Printf("Tags:   %v\n", mem.Tags)
		fmt.Printf("Access: %d\n", mem.AccessCount)
		fmt.Printf("Created: %s\n", mem.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Updated: %s\n", mem.UpdatedAt.Format("2006-01-02 15:04:05"))
		if mem.ExpiresAt != nil {
			fmt.Printf("Expires: %s\n", mem.ExpiresAt.Format("2006-01-02 15:04:05"))
		}
		fmt.Printf("\n%s\n", mem.Content)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(readCmd)
}
