package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var linkCmd = &cobra.Command{
	Use:   "link <source_id> <target_id>",
	Short: "Create a bidirectional link between two memories",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		if err := s.LinkMemories(args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("Linked %s <-> %s\n", args[0][:8], args[1][:8])
		return nil
	},
}

var unlinkCmd = &cobra.Command{
	Use:   "unlink <source_id> <target_id>",
	Short: "Remove a bidirectional link between two memories",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		if err := s.UnlinkMemories(args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("Unlinked %s <-> %s\n", args[0][:8], args[1][:8])
		return nil
	},
}

var resolveLinksCmd = &cobra.Command{
	Use:   "resolve-links",
	Short: "Scan all memories and resolve [[wikilink]] backlinks",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		count, err := s.ResolveBacklinks()
		if err != nil {
			return err
		}
		fmt.Printf("Resolved backlinks for %d memories\n", count)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(linkCmd)
	rootCmd.AddCommand(unlinkCmd)
	rootCmd.AddCommand(resolveLinksCmd)
}
