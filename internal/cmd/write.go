package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	writeType     string
	writeScope    string
	writeTags     string
	writeSource   string
	writeCategory string
)

var writeCmd = &cobra.Command{
	Use:   "write [content]",
	Short: "Write a new memory (defaults to inbox)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		content := strings.Join(args, " ")
		var tags []string
		if writeTags != "" {
			tags = strings.Split(writeTags, ",")
		}

		cat := store.Category(writeCategory)
		if writeCategory != "" {
			if err := validateCategory(cat); err != nil {
				return err
			}
			mem, err := s.Write(content, store.PhaseOrganized, cat, writeScope, tags, writeSource)
			if err != nil {
				return err
			}
			fmt.Printf("Created %s/%s memory: %s\n", cat, mem.Phase, mem.ID)
		} else {
			mem, err := s.WriteToInbox(content, writeScope, tags, writeSource)
			if err != nil {
				return err
			}
			fmt.Printf("Created inbox memory: %s\n", mem.ID)
		}
		return nil
	},
}

func init() {
	writeCmd.Flags().StringVar(&writeCategory, "category", "", "Category: soul,people,project,date,knowledge,feedback,preferences,inbox")
	writeCmd.Flags().StringVar(&writeScope, "scope", "global", "Memory scope")
	writeCmd.Flags().StringVar(&writeTags, "tags", "", "Comma-separated tags")
	writeCmd.Flags().StringVar(&writeSource, "source", "manual", "Memory source")
	rootCmd.AddCommand(writeCmd)
}

func validateCategory(cat store.Category) error {
	for _, c := range append(append(store.AllCategories, store.CategoryInbox), store.CategoryReminders) {
		if c == cat {
			return nil
		}
	}
	return fmt.Errorf("invalid category %q", cat)
}
