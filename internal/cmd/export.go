package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	exportOutput string
	exportType   string
	exportScope  string
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export memories to JSONL file",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		opts := store.ListOptions{
			Type:  store.MemoryType(exportType),
			Scope: exportScope,
		}
		memories, err := s.List(opts)
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			fmt.Println("No memories to export.")
			return nil
		}

		var f *os.File
		if exportOutput == "" || exportOutput == "-" {
			f = os.Stdout
		} else {
			f, err = os.Create(exportOutput)
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer f.Close()
		}

		enc := json.NewEncoder(f)
		for _, mem := range memories {
			if err := enc.Encode(mem); err != nil {
				return fmt.Errorf("encode memory: %w", err)
			}
		}
		if exportOutput != "" && exportOutput != "-" {
			fmt.Printf("Exported %d memories to %s\n", len(memories), exportOutput)
		}
		return nil
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportOutput, "output", "", "Output file (default: stdout)")
	exportCmd.Flags().StringVar(&exportType, "type", "", "Filter by type: short or long")
	exportCmd.Flags().StringVar(&exportScope, "scope", "", "Filter by scope")
	rootCmd.AddCommand(exportCmd)
}
