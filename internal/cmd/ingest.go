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
			for _, mem := range memories {
				existing, _ := s.Search(store.SearchOptions{Query: contentHash(mem.Content), Scope: mem.Scope})
				if len(existing) > 0 {
					continue
				}
				if _, err := s.Write(mem.Content, mem.Type, mem.Scope, mem.Tags, mem.Source); err != nil {
					fmt.Fprintf(os.Stderr, "Error writing memory from %s: %v\n", name, err)
					continue
				}
				count++
			}
			fmt.Printf("Ingested %d memories from %s\n", count, name)
			total += count
		}
		fmt.Printf("\nTotal: %d new memories ingested\n", total)
		return nil
	},
}

func init() {
	ingestCmd.Flags().StringVar(&ingestSource, "source", "all", "Source: claude, car-agent, fingersaver, logseq, obsidian, all")
	ingestCmd.Flags().StringVar(&ingestPath, "path", "", "Custom path for logseq/obsidian vault")
	rootCmd.AddCommand(ingestCmd)
}

func getAdapters(customPath string) map[string]ingest.Adapter {
	home := config.MustHomeDir()
	return map[string]ingest.Adapter{
		"claude":      &ingest.ClaudeAdapter{Path: filepath.Join(home, ".claude")},
		"car-agent":   &ingest.CarAgentAdapter{Path: filepath.Join(home, ".car-agent")},
		"fingersaver": &ingest.FingersaverAdapter{Path: filepath.Join(home, ".fingersaver")},
		"logseq":      &ingest.LogseqAdapter{Path: customPath},
		"obsidian":    &ingest.ObsidianAdapter{Path: customPath},
	}
}

func parseSources(source string) []string {
	if source == "all" {
		return []string{"claude", "car-agent", "fingersaver", "logseq", "obsidian"}
	}
	return strings.Split(source, ",")
}

func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:8])
}
