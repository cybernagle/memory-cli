package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	eventsID    string
	eventsLimit int
)

// eventsCmd exposes the append-only event log (raw_entries) — the single source of truth
// every derived view is rebuilt from. Frontends read projections; this is where you audit
// what actually happened, and trace any memory back to its originating event.
var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "View the append-only event log (source of truth for all derived views)",
	Long: `List events from the append-only raw_entries log, in append order (rowid = sequence).

Events are immutable and never deleted; every memory, tag, link and search index is
derived from this log (see: memory rebuild). Use --id to inspect one event, or pass a
memory's raw_entry_id (shown as raw_entry_id by memory list / search APIs) to see the
event a specific memory came from.`,
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

		entries, err := s.ListRawEntries()
		if err != nil {
			return err
		}

		// --id filter: accept a full event id (content hash) or a unique prefix, and also
		// a memory id so "memory events --id <memory-id>" does the lookup in one step.
		filter := strings.ToLower(strings.TrimSpace(eventsID))
		if filter != "" {
			memID := filter
			if mem, err := s.FindByID(memID); err == nil && mem.RawEntryID != "" {
				filter = strings.ToLower(mem.RawEntryID)
			}
		}

		shown := 0
		for i, e := range entries {
			seq := i + 1
			if filter != "" && !strings.HasPrefix(strings.ToLower(e.ID), filter) {
				continue
			}
			shown++
			if eventsLimit > 0 && shown > eventsLimit {
				shown--
				break
			}
			prov := []string{}
			for _, p := range []struct{ k, v string }{
				{"project", e.Project}, {"session", e.SessionID}, {"tmux", e.TmuxSession},
				{"branch", e.GitBranch}, {"prompt", e.PromptID},
			} {
				if p.v != "" {
					prov = append(prov, p.k+":"+p.v)
				}
			}
			content := e.Content
			if len([]rune(content)) > 72 {
				content = string([]rune(content)[:72]) + "…"
			}
			fmt.Printf("#%-6d %s  %s\n        %s\n", seq, e.IngestedAt, e.ID[:12], content)
			if len(prov) > 0 {
				fmt.Printf("        [%s]\n", strings.Join(prov, ", "))
			}
		}
		fmt.Printf("\n%d events total, %d shown\n", len(entries), shown)
		return nil
	},
}

func init() {
	eventsCmd.Flags().StringVar(&eventsID, "id", "", "filter by event id (content hash), prefix, or memory id")
	eventsCmd.Flags().IntVar(&eventsLimit, "limit", 20, "max events to show (0 = all)")
	rootCmd.AddCommand(eventsCmd)
}
