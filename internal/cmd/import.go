package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/store"
)

var importInput string

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import memories from JSONL file",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		var f *os.File
		if importInput == "" || importInput == "-" {
			f = os.Stdin
		} else {
			f, err = os.Open(importInput)
			if err != nil {
				return fmt.Errorf("open input file: %w", err)
			}
			defer f.Close()
		}

		dec := json.NewDecoder(f)
		count := 0
		for {
			var mem store.Memory
			if err := dec.Decode(&mem); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("malformed JSON at record %d: %w", count+1, err)
			}
			if _, err := s.Write(mem.Content, store.PhaseInbox, store.CategoryInbox, mem.Scope, mem.Tags, mem.Source); err != nil {
				fmt.Fprintf(os.Stderr, "Error importing memory: %v\n", err)
				continue
			}
			count++
		}
		fmt.Printf("Imported %d memories\n", count)
		return nil
	},
}

func init() {
	importCmd.Flags().StringVar(&importInput, "input", "", "Input JSONL file (default: stdin)")
	rootCmd.AddCommand(importCmd)
}
