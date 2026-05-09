package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	searchTags     string
	searchScope    string
	searchCategory string
	searchFrom     string
	searchTo       string
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search memories by keyword",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		opts := store.SearchOptions{
			Query: args[0],
			Scope: searchScope,
		}
		if searchTags != "" {
			opts.Tags = strings.Split(searchTags, ",")
		}
		if searchFrom != "" {
			t, err := time.Parse("2006-01-02", searchFrom)
			if err != nil {
				return fmt.Errorf("invalid --from date: %w", err)
			}
			opts.From = &t
		}
		if searchTo != "" {
			t, err := time.Parse("2006-01-02", searchTo)
			if err != nil {
				return fmt.Errorf("invalid --to date: %w", err)
			}
			endOfDay := t.Add(24*time.Hour - time.Nanosecond)
			opts.To = &endOfDay
		}

		memories, err := s.Search(opts)
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			fmt.Println("No matching memories found.")
			return nil
		}
		for _, m := range memories {
			preview := truncateRunes(m.Content, 80)
			preview = strings.ReplaceAll(preview, "\n", " ")
			fmt.Printf("%-8s %-10s %-10s %s\n", m.ID[:8], m.Phase, m.Category, preview)
		}
		fmt.Printf("\nFound: %d memories\n", len(memories))
		return nil
	},
}

func init() {
	searchCmd.Flags().StringVar(&searchTags, "tags", "", "Comma-separated tags to match")
	searchCmd.Flags().StringVar(&searchScope, "scope", "", "Filter by scope")
	searchCmd.Flags().StringVar(&searchCategory, "category", "", "Filter by category")
	searchCmd.Flags().StringVar(&searchFrom, "from", "", "Filter from date (YYYY-MM-DD)")
	searchCmd.Flags().StringVar(&searchTo, "to", "", "Filter to date inclusive (YYYY-MM-DD)")
	rootCmd.AddCommand(searchCmd)
}
