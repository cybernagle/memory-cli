package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

var entityBuildBatch int

// entityBuildCmd is a one-off backlog drain for EntityExtractionTask. The daemon processes
// only entityExtractPerTick memories per tick, which (especially before the List(500) scan-bug
// fix) left ~11k of older organized/processed memories without entities. This command runs the
// extraction task in a loop with a large Limit until the backlog is empty or --batch is reached
// per iteration, printing progress.
//
// Idempotent: each memory is marked consumed after extraction (consumed_mask bit), so re-running
// picks up only what's left. Safe to interrupt (Ctrl-C) and resume — progress persists per
// memory, not per batch.
var entityBuildCmd = &cobra.Command{
	Use:   "entity-build",
	Short: "One-off: extract entities for the full backlog (drain everything the daemon hasn't reached)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		dbPath := cfg.Storage.SQLitePath
		if dbPath == "" {
			dbPath = config.SQLiteDefaultPath()
		}
		s, err := store.NewSqliteStore(dbPath)
		if err != nil {
			return fmt.Errorf("open sqlite: %w", err)
		}
		defer s.Close()

		if entityBuildBatch <= 0 {
			entityBuildBatch = 500
		}

		llmClient, err := llm.NewClient(llm.Config{})
		if err != nil {
			return fmt.Errorf("LLM client (need MEMORY_LLM_API_KEY): %w", err)
		}

		task := &daemon.EntityExtractionTask{
			LLM:   llmClient,
			Store: s,
			Limit: entityBuildBatch, // large per-iteration cap; loop drains the whole backlog
		}

		// Snapshot the starting backlog so we can report how much drained.
		startBacklog, _ := s.ListUnconsumedInPhase("entity-extract",
			[]store.Phase{store.PhaseOrganized, store.PhaseProcessed}, 1<<30)
		startCount := len(startBacklog)
		fmt.Printf("Entity backlog: %d memories to process (batch %d/iter)\n", startCount, entityBuildBatch)
		if startCount == 0 {
			fmt.Println("Nothing to do — backlog already empty.")
			return nil
		}

		totalExtracted := 0
		round := 0
		start := time.Now()
		for {
			round++
			n, err := task.Run(s)
			if err != nil {
				fmt.Printf("round %d error: %v (stopping; resume by re-running)\n", round, err)
				break
			}
			if n == 0 {
				break // backlog drained
			}
			totalExtracted += n
			remaining, _ := s.ListUnconsumedInPhase("entity-extract",
				[]store.Phase{store.PhaseOrganized, store.PhaseProcessed}, 1<<30)
			fmt.Printf("round %d: extracted %d entity mentions, %d backlog remaining (%.0fs elapsed)\n",
				round, n, len(remaining), time.Since(start).Seconds())
			if len(remaining) == 0 {
				break
			}
		}

		// Final coverage report.
		fmt.Printf("\nDone: %d entity mentions extracted over %d rounds (%.0fs).\n", totalExtracted, round, time.Since(start).Seconds())
		return nil
	},
}

func init() {
	entityBuildCmd.Flags().IntVar(&entityBuildBatch, "batch", 500, "memories per iteration (default 500)")
	rootCmd.AddCommand(entityBuildCmd)
}
