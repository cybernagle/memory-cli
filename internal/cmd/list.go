package cmd

import (
	"fmt"
	"sort"
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
		if err := validateType(listType); err != nil {
			return err
		}
		s, err := getStore()
		if err != nil {
			return err
		}
		allOpts := store.ListOptions{
			Type:   store.MemoryType(listType),
			Scope:  listScope,
			Source: listSource,
		}
		all, err := s.List(allOpts)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Println("No memories found.")
			return nil
		}

		sort.Slice(all, func(i, j int) bool {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		})

		total := len(all)
		memories := all
		if listLimit > 0 && len(memories) > listLimit {
			memories = memories[:listLimit]
		}

		fmt.Printf("%-8s %-6s %-10s %-10s %-20s %s\n", "ID", "Type", "Scope", "Source", "Created", "Preview")
		fmt.Println(strings.Repeat("-", 80))
		for _, m := range memories {
			preview := truncateRunes(m.Content, 40)
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
		fmt.Printf("\nShowing %d of %d memories\n", len(memories), total)
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

func validateType(t string) error {
	if t != "" && t != "short" && t != "long" {
		return fmt.Errorf("invalid type %q: must be 'short' or 'long'", t)
	}
	return nil
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
