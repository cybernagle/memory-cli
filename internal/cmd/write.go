package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	writeType   string
	writeScope  string
	writeTags   string
	writeSource string
)

var writeCmd = &cobra.Command{
	Use:   "write [content]",
	Short: "Write a new memory",
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
		memType := store.MemoryType(writeType)
		if writeType != "" && memType != store.ShortTerm && memType != store.LongTerm {
			return fmt.Errorf("invalid type %q: must be 'short' or 'long'", writeType)
		}
		if memType == "" {
			memType = store.LongTerm
		}
		mem, err := s.Write(content, memType, writeScope, tags, writeSource)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s memory: %s\n", mem.Type, mem.ID)
		return nil
	},
}

func init() {
	writeCmd.Flags().StringVar(&writeType, "type", "long", "Memory type: short or long")
	writeCmd.Flags().StringVar(&writeScope, "scope", "global", "Memory scope")
	writeCmd.Flags().StringVar(&writeTags, "tags", "", "Comma-separated tags")
	writeCmd.Flags().StringVar(&writeSource, "source", "manual", "Memory source")
	rootCmd.AddCommand(writeCmd)
}
