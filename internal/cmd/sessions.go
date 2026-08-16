package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	sessionsProject string
	sessionsEntity  string
	sessionsSession string
	sessionsLimit   int
	sessionsBuild   int
)

// sessionsCmd reads (and optionally builds) the per-session projection: what task each
// session performed, which entity+facet it revolved around, a summary, and extracted
// reusable lessons. Built incrementally by the daemon's SessionDigestTask.
var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List per-session task/experience digests (session_views projection)",
	Long: `List session digests: task, entity, facet, summary and lessons per session.

By default shows already-digested sessions (built by the daemon's
session-digest task). Use --build N to digest up to N pending sessions
right now (first-run backfill; each session is one LLM call).`,
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

		if sessionsBuild > 0 {
			client, err := llm.NewClient(llm.Config{})
			if err != nil {
				return fmt.Errorf("LLM unavailable (needed for --build): %w", err)
			}
			n, err := (&daemon.SessionDigestTask{Store: s, LLM: client, Limit: sessionsBuild}).Run(s)
			if err != nil {
				return err
			}
			fmt.Printf("digested %d sessions\n", n)
		}

		views, err := s.ListSessionViews(store.SessionViewFilter{
			SessionID: sessionsSession,
			Project:   sessionsProject,
			Entity:    sessionsEntity,
			Limit:     sessionsLimit,
		})
		if err != nil {
			return err
		}
		if len(views) == 0 {
			fmt.Println("No session digests yet. Run `memory sessions --build 20` to create the first batch.")
			return nil
		}
		for _, v := range views {
			fmt.Printf("── %s ──\n", v.SessionID)
			fmt.Printf("  %s | project=%s", shortTime(v.LastSeen), v.Project)
			if v.TmuxSession != "" {
				fmt.Printf(" | tmux=%s", v.TmuxSession)
			}
			fmt.Printf(" | %d memories\n", v.MemoryCount)
			fmt.Printf("  task:   %s\n", v.Task)
			if v.Entity != "" {
				fmt.Printf("  entity: %s", v.Entity)
				if v.Facet != "" {
					fmt.Printf(" / %s", v.Facet)
				}
				fmt.Println()
			}
			fmt.Printf("  %s\n", v.Summary)
			var lessons []string
			if json.Unmarshal([]byte(v.Lessons), &lessons) == nil {
				for _, l := range lessons {
					fmt.Printf("  ⚑ %s\n", l)
				}
			}
		}
		total, _ := s.SessionViewCount()
		fmt.Printf("\n%d sessions shown, %d digested total\n", len(views), total)
		return nil
	},
}

func shortTime(rfc3339 string) string {
	if t, err := time.Parse(time.RFC3339, rfc3339); err == nil {
		return t.Format("2006-01-02 15:04")
	}
	return rfc3339
}

func init() {
	sessionsCmd.Flags().StringVar(&sessionsProject, "project", "", "filter by project")
	sessionsCmd.Flags().StringVar(&sessionsEntity, "entity", "", "filter by entity (substring match)")
	sessionsCmd.Flags().StringVar(&sessionsSession, "session", "", "show one session by id")
	sessionsCmd.Flags().IntVar(&sessionsLimit, "limit", 20, "max sessions to show")
	sessionsCmd.Flags().IntVar(&sessionsBuild, "build", 0, "digest up to N pending sessions now (LLM backfill)")
	rootCmd.AddCommand(sessionsCmd)
}
