package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var cfgPath string

var version = "0.2.0"

var rootCmd = &cobra.Command{
	Use:   "memory",
	Short: "Unified memory management for AI agents",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	home := config.MustHomeDir()
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", filepath.Join(home, ".memory", "config.yaml"), "config file path")
	rootCmd.Version = version
}

func getStore() (store.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.Storage.Backend == "sqlite" {
		s, err := store.NewSqliteStoreFromConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		return s, nil
	}
	s := store.New(cfg)
	if err := s.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}
	return s, nil
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
