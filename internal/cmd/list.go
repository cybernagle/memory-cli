package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	listCategory string
	listScope    string
	listSource   string
	listLimit    int
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
			Category: store.Category(listCategory),
			Scope:    listScope,
			Source:   listSource,
		}
		all, err := s.List(opts)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Println("No memories found.")
			return nil
		}

		total := len(all)
		memories := all
		if listLimit > 0 && len(memories) > listLimit {
			memories = memories[:listLimit]
		}

		fmt.Printf("%-8s %-10s %-10s %-10s %-20s %s\n", "ID", "Phase", "Category", "Source", "Created", "Preview")
		fmt.Println(strings.Repeat("-", 100))
		for _, m := range memories {
			preview := truncateRunes(m.Content, 40)
			preview = strings.ReplaceAll(preview, "\n", " ")
			fmt.Printf("%-8s %-10s %-10s %-10s %-20s %s\n",
				m.ID[:8],
				m.Phase,
				m.Category,
				m.Source,
				m.CreatedAt.Format("2006-01-02 15:04"),
				preview,
			)
		}
		fmt.Printf("\nShowing %d of %d memories\n", len(memories), total)
		return nil
	},
}

func init() {
	listCmd.Flags().StringVar(&listCategory, "category", "", "Filter by category: inbox,people,project,date,...")
	listCmd.Flags().StringVar(&listScope, "scope", "", "Filter by scope")
	listCmd.Flags().StringVar(&listSource, "source", "", "Filter by source")
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "Max results")
	rootCmd.AddCommand(listCmd)
}
