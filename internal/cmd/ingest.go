package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/ingest"
	"github.com/cybernagle/memory-cli/internal/store"
)

var ingestSource string
var ingestPath string

var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Ingest memories from external sources",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		adapters := getAdapters(ingestPath)
		sources := parseSources(ingestSource)

		total := 0
		for _, name := range sources {
			adapter, ok := adapters[name]
			if !ok {
				fmt.Fprintf(os.Stderr, "Unknown source: %s\n", name)
				continue
			}
			memories, err := adapter.Ingest()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error ingesting %s: %v\n", name, err)
				continue
			}
			count := 0
			updated := 0
			for _, mem := range memories {
				hash := contentHash(mem.Content)
				existing, err := s.FindByHash(hash)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking dedup for %s: %v\n", name, err)
					continue
				}
				// Even when the memory already exists, we still call IngestMemory: the store's
				// upsert path backfills any provenance columns (role, git_branch, prompt_id,
				// message_uuid, ...) that are empty on the existing row but present in this
				// (richer) ingest. Re-importing is therefore idempotent AND progressively
				// enriches older data as ingest code captures more fields.
				if err := s.IngestMemory(mem); err != nil {
					fmt.Fprintf(os.Stderr, "Error writing memory from %s: %v\n", name, err)
					continue
				}
				if existing != nil {
					updated++
				} else {
					count++
				}
			}
			if count+updated > 0 {
				if ss, ok := s.(*store.SqliteStore); ok {
					ss.LogActivity("ingest", "", name, fmt.Sprintf("%d new, %d updated", count, updated))
				}
			}
			fmt.Printf("Ingested %d memories from %s (%d updated)\n", count, name, updated)
			total += count
		}
		fmt.Printf("\nTotal: %d new memories ingested\n", total)
		return nil
	},
}

func init() {
	ingestCmd.Flags().StringVar(&ingestSource, "source", "all", "Source: claude, conversations, car-agent, fingersaver, logseq, obsidian, all")
	ingestCmd.Flags().StringVar(&ingestPath, "path", "", "Custom path for logseq/obsidian vault")
	rootCmd.AddCommand(ingestCmd)
}

func getAdapters(customPath string) map[string]ingest.Adapter {
	home := config.MustHomeDir()
	return map[string]ingest.Adapter{
		"claude":        &ingest.ClaudeAdapter{Path: filepath.Join(home, ".claude")},
		"conversations": &ingest.ConversationsAdapter{Path: filepath.Join(home, ".claude", "projects")},
		"zcode":         &ingest.ZcodeAdapter{Path: filepath.Join(home, ".zcode", "cli", "rollout")},
		"car-agent":     &ingest.CarAgentAdapter{Path: filepath.Join(home, ".car-agent")},
		"fingersaver":   &ingest.FingersaverAdapter{Path: filepath.Join(home, ".fingersaver")},
		"logseq":        &ingest.LogseqAdapter{Path: customPath},
		"obsidian":      &ingest.ObsidianAdapter{Path: customPath},
	}
}

func parseSources(source string) []string {
	if source == "all" {
		return []string{"claude", "conversations", "zcode", "car-agent", "fingersaver", "logseq", "obsidian"}
	}
	return strings.Split(source, ",")
}

func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}
