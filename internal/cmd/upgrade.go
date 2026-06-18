package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [id]",
	Short: "Upgrade a short-term memory to long-term (via LLM pipeline)",
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
		if mem.Phase == store.PhaseOrganized {
			fmt.Printf("Memory %s is already organized\n", mem.ID)
			return nil
		}
		// Re-write as processed so L3 consolidate can promote to organized via LLM
		_, err = s.Write(mem.Content, store.PhaseProcessed, mem.Category, mem.Scope, mem.Tags, mem.Source)
		if err != nil {
			return err
		}
		if err := s.Delete(mem.ID); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: failed to delete original %s: %v\n", mem.ID, err)
		}
		fmt.Printf("Queued memory for LLM pipeline (was %s)\n", mem.Phase)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}
