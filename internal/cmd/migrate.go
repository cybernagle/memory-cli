package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate memories from file store to SQLite",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		dbPath := cfg.Storage.SQLitePath
		if dbPath == "" {
			dbPath = config.SQLiteDefaultPath()
		}

		fileStore := store.New(cfg)
		if err := fileStore.Init(); err != nil {
			return fmt.Errorf("init file store: %w", err)
		}

		sqliteStore, err := store.NewSqliteStore(dbPath)
		if err != nil {
			return fmt.Errorf("open sqlite: %w", err)
		}
		defer sqliteStore.Close()

		result, err := store.MigrateFromFiles(fileStore, sqliteStore)
		if err != nil {
			return err
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
