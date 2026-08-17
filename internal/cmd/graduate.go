package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var graduatePB string

// graduateCmd manages the graduation queue: business facts waiting to leave memory and
// enter the project's system of record (PocketBase, CRM, ...). Boundary rule: business
// data lives in the business system; memory keeps only a pointer.
var graduateCmd = &cobra.Command{
	Use:   "graduate",
	Short: "Manage the graduation queue (business facts → project system of record)",
	Long: `Graduation queue: business facts outgrow memory and must be archived into the
project's system of record (PocketBase ...). memory never duplicates business
data — a fact graduates with a pointer back.

memory graduate <project> "fact..."            — enqueue
memory graduate [--all]                        — list the queue (pending first)
memory graduate done <id> --pb pb://...        — mark archived, keep pointer`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStateStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if len(args) >= 2 && args[0] == "done" {
			id, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("bad id: %w", err)
			}
			if err := s.CompleteGraduation(id, graduatePB); err != nil {
				return err
			}
			fmt.Printf("✓ graduation #%d archived → %s\n", id, graduatePB)
			return nil
		}

		if len(args) >= 2 {
			g, err := s.AddGraduation(args[0], strings.Join(args[1:], " "), "")
			if err != nil {
				return err
			}
			fmt.Printf("✓ queued #%d: %s — %s\n", g.ID, g.Project, g.Fact)
			return nil
		}

		grads, err := s.ListGraduations(!graduateListAll)
		if err != nil {
			return err
		}
		if len(grads) == 0 {
			fmt.Println("Queue empty.")
			return nil
		}
		for _, g := range grads {
			status := "→ 待归档"
			if !g.Pending() {
				status = "✓ " + g.PBPointer
			}
			fmt.Printf("#%-4d %s — %s\n      %s · %s\n", g.ID, g.Project, g.Fact, g.CreatedAt[:10], status)
		}
		return nil
	},
}

var graduateListAll bool

func init() {
	graduateCmd.Flags().BoolVar(&graduateListAll, "all", false, "include archived entries")
	graduateCmd.Flags().StringVar(&graduatePB, "pb", "", "pb pointer for 'done' (e.g. pb://ruifulai/feedback/rec123)")
	rootCmd.AddCommand(graduateCmd)
}
