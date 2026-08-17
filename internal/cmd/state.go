package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	stateVersion  string
	stateBranch   string
	stateCommit   string
	statePhase    string
	stateBlockers string
	stateNext     string
	stateNotes    string
	stateBy       string
	stateSession  string
)

// stateCmd reads/writes the shared per-project working state (project_states projection).
// Contract: agents READ at session start (verify stale entries against git) and WRITE at
// milestones/session end.
var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "Shared per-project working state for multi-agent handoff (get/set/list)",
	Long: `Shared working state between agent sessions.

memory state                — list all projects (one line each)
memory state get <project>  — one project's full state (+ history with --history)
memory state set <project> --version v26 --commit abc1234 ...  — report current state

Contract: read at session start, write at milestones/session end,
never trust a STALE entry without verifying against git.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStateStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if len(args) == 0 {
			if gaps, err := s.CoverageGaps(); err == nil && len(gaps) > 0 {
				fmt.Println("⚠️  Coverage gaps(会话有进展但状态未上报):")
				for _, g := range gaps {
					if g.LastState == "" {
						fmt.Printf("    %s — 无状态记录;最近工作 %.0fh 前:%s\n", g.Project, g.DeltaHours, orDash(g.TaskHead))
					} else {
						fmt.Printf("    %s — 状态落后最近会话 %.0fh;最近工作:%s\n", g.Project, g.DeltaHours, orDash(g.TaskHead))
					}
				}
				fmt.Println()
			}
			states, err := s.ListProjectStates()
			if err != nil {
				return err
			}
			if len(states) == 0 {
				fmt.Println("No project states yet. Set one: memory state set <project> --version ...")
				return nil
			}
			for _, ps := range states {
				stale := ""
				if ps.Stale {
					stale = "  ⚠️STALE"
				}
				fmt.Printf("%-14s %s @ %s(%s) · %s · %.1fh ago by %s%s\n",
					ps.Project, orDash(ps.Version), orDash(ps.Branch), ps.CommitShort(),
					orDash(ps.Phase), ps.AgeHours, orDash(ps.UpdatedBy), stale)
				if len(ps.NextActions) > 0 {
					fmt.Printf("    next: %s\n", strings.Join(ps.NextActions, " / "))
				}
				if len(ps.Blockers) > 0 {
					fmt.Printf("    blockers: %s\n", strings.Join(ps.Blockers, " / "))
				}
			}
			return nil
		}

		switch args[0] {
		case "get":
			if len(args) < 2 {
				return fmt.Errorf("usage: memory state get <project>")
			}
			ps, err := s.GetProjectState(args[1])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(ps, "", "  ")
			fmt.Println(string(data))
			if ps.Stale {
				fmt.Println("⚠️ STALE: 超过 24h,动工前先 git 核实。")
			}
			return nil
		case "set":
			if len(args) < 2 {
				return fmt.Errorf("usage: memory state set <project> --version ... ")
			}
			ps, err := s.SetProjectState(store.StateInput{
				Project: args[1], Version: stateVersion, Branch: stateBranch, Commit: stateCommit,
				Phase: statePhase, Blockers: splitCSV(stateBlockers), NextActions: splitCSV(stateNext),
				Notes: stateNotes, UpdatedBy: orDefault(stateBy, "cli"), SessionID: stateSession,
			})
			if err != nil {
				return err
			}
			fmt.Printf("✓ %s %s @ %s(%s) · %s\n", ps.Project, ps.Version, ps.Branch, ps.CommitShort(), ps.Phase)
			return nil
		default:
			return fmt.Errorf("unknown subcommand %q (get|set)", args[0])
		}
	},
}

func openStateStore() (*store.SqliteStore, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	dbPath := cfg.Storage.SQLitePath
	if dbPath == "" {
		dbPath = config.SQLiteDefaultPath()
	}
	return store.NewSqliteStore(dbPath)
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDash(s string) string { return orDefault(s, "-") }
func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func init() {
	stateCmd.Flags().StringVar(&stateVersion, "version", "", "current version label (v26)")
	stateCmd.Flags().StringVar(&stateBranch, "branch", "", "git branch")
	stateCmd.Flags().StringVar(&stateCommit, "commit", "", "HEAD commit hash")
	stateCmd.Flags().StringVar(&statePhase, "phase", "", "阶段: 设计/开发/测试/部署/验收")
	stateCmd.Flags().StringVar(&stateBlockers, "blockers", "", "comma-separated blockers")
	stateCmd.Flags().StringVar(&stateNext, "next", "", "comma-separated next actions")
	stateCmd.Flags().StringVar(&stateNotes, "notes", "", "free text for the next agent")
	stateCmd.Flags().StringVar(&stateBy, "by", "", "who reports (agent name)")
	stateCmd.Flags().StringVar(&stateSession, "session", "", "reporting session id")
	rootCmd.AddCommand(stateCmd)
}
