package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/llm"
)

var usageRecent int

// usageCmd reports prompt/token usage from the shared ~/.makro/prompt_usage.db.
var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Report prompt/token usage (calls, tokens, duplicates, over-frequency)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if usageRecent <= 0 {
			usageRecent = 10
		}
		r, err := llm.QueryReport(usageRecent)
		if err != nil {
			return fmt.Errorf("query usage: %w", err)
		}

		fmt.Printf("Prompt usage  (~/.makro/prompt_usage.db)\n")
		fmt.Printf("─────────────────────────────────────────────\n")
		fmt.Printf("Total calls:     %d   (duplicates: %d, errors: %d)\n", r.TotalCalls, r.Duplicates, r.Errors)
		fmt.Printf("Total tokens:    %d   (prompt %d / completion %d)\n", r.TotalTokens, r.PromptTokens, r.CompletionTokens)

		if len(r.OverFreq) > 0 {
			fmt.Printf("\n⚠ OVER-FREQUENCY (recent window):\n")
			for _, f := range r.OverFreq {
				fmt.Printf("  - %s\n", f)
			}
		}

		fmt.Printf("\nBy function:\n")
		for _, s := range r.ByFunction {
			fmt.Printf("  %-14s %4d calls   %8d tokens\n", s.Key, s.Count, s.Total)
		}

		fmt.Printf("\nBy model:\n")
		for _, s := range r.ByModel {
			fmt.Printf("  %-18s %4d calls   %8d tokens\n", s.Key, s.Count, s.Total)
		}

		if len(r.Recent) > 0 {
			fmt.Printf("\nRecent %d calls (newest first):\n", len(r.Recent))
			for _, e := range r.Recent {
				status := "ok"
				if e.Error != "" {
					status = "ERR"
				}
				fmt.Printf("  [%s] %-12s %-14s in=%-5d out=%-5d %dms  %s\n",
					e.Session, e.Function, e.Model, e.PromptTokens, e.CompletionTokens, e.DurationMs, status)
			}
		}
		return nil
	},
}

func init() {
	usageCmd.Flags().IntVar(&usageRecent, "recent", 10, "number of recent calls to show")
	rootCmd.AddCommand(usageCmd)
}
