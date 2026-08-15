package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	rebuildFull bool
	rebuildYes  bool
)

// rebuildCmd rebuilds derived views from the append-only event log (raw_entries) — the
// event-sourcing "drop the view, replay the log" recovery path (DDIA ch3).
//
// Default mode is safe: only the deterministic derived structures (FTS index, wiki-links,
// keyword tags) are rebuilt; memory rows are untouched.
//
// --full is DESTRUCTIVE to processing state: the derived layer is wiped and replayed from
// events. Content and provenance are restored exactly; phase and LLM refinements are
// re-derived by the daemon pipelines afterwards (results may differ — the projector is
// non-deterministic).
var rebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Rebuild derived views (FTS/links/tags) from the event log; --full replays everything",
	Long: `Rebuild derived views from the append-only event log.

Default (safe): rebuild the FTS index, wiki-links and keyword tags.
Memory rows, phases and LLM refinements are untouched. Use this when an index is
corrupted or a search migration went wrong.

--full --yes: wipe the derived layer (memories, tags, links, FTS, entities) and replay
every event in sequence order. Content and provenance are restored exactly, but all
memories land in inbox with consumed_mask=0 — the daemon pipelines (consolidate,
entity, profile) re-derive processing state, and because they are LLM-based, results
may differ from the originals. reminders and activity_log are not derived data and
are never touched.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if rebuildFull && !rebuildYes {
			return fmt.Errorf("--full wipes the derived layer and re-derives processing state; pass --yes to confirm")
		}

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

		var stats *store.RebuildStats
		if rebuildFull {
			fmt.Println("Replaying event log (destructive to processing state)...")
			stats, err = s.RebuildFromEvents()
		} else {
			fmt.Println("Rebuilding derived indexes (safe)...")
			stats, err = s.RebuildIndexes()
		}
		if err != nil {
			return err
		}
		fmt.Printf("rebuild[%s]: %s\n", stats.Mode, stats.Describe())
		if rebuildFull {
			fmt.Println("Memories are back in inbox; run `memory serve` so daemon pipelines re-organize them.")
		}
		return nil
	},
}

func init() {
	rebuildCmd.Flags().BoolVar(&rebuildFull, "full", false, "wipe derived layer and replay all events (destructive to processing state)")
	rebuildCmd.Flags().BoolVar(&rebuildYes, "yes", false, "confirm the destructive --full rebuild")
	rootCmd.AddCommand(rebuildCmd)
}
