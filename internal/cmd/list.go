package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	listType   string
	listScope  string
	listSource string
	listLimit  int
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List memories",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		opts := store.ListOptions{
			Type:   store.MemoryType(listType),
			Scope:  listScope,
			Source: listSource,
			Limit:  listLimit,
		}
		memories, err := s.List(opts)
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			fmt.Println("No memories found.")
			return nil
		}
		fmt.Printf("%-8s %-6s %-10s %-10s %-20s %s\n", "ID", "Type", "Scope", "Source", "Created", "Preview")
		fmt.Println(strings.Repeat("-", 80))
		for _, m := range memories {
			preview := m.Content
			if len(preview) > 40 {
				preview = preview[:40] + "..."
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			fmt.Printf("%-8s %-6s %-10s %-10s %-20s %s\n",
				m.ID,
				m.Type,
				m.Scope,
				m.Source,
				m.CreatedAt.Format("2006-01-02 15:04"),
				preview,
			)
		}
		fmt.Printf("\nTotal: %d memories\n", len(memories))
		return nil
	},
}

func init() {
	listCmd.Flags().StringVar(&listType, "type", "", "Filter by type: short or long")
	listCmd.Flags().StringVar(&listScope, "scope", "", "Filter by scope")
	listCmd.Flags().StringVar(&listSource, "source", "", "Filter by source")
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "Max results")
	rootCmd.AddCommand(listCmd)
}
