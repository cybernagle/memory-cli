package store

import "fmt"

type MigrationResult struct {
	Total    int `json:"total"`
	Migrated int `json:"migrated"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
}

func MigrateFromFiles(fileStore *FileStore, sqliteStore *SqliteStore) (*MigrationResult, error) {
	all, err := fileStore.List(ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("read file store: %w", err)
	}

	result := &MigrationResult{Total: len(all)}

	for _, mem := range all {
		existing, _ := sqliteStore.FindByID(mem.ID)
		if existing != nil {
			result.Skipped++
			continue
		}
		if err := sqliteStore.InsertMemory(mem); err != nil {
			result.Errors++
			continue
		}
		// Backfill activity log for heatmap/histogram
		sqliteStore.db.Exec("INSERT INTO activity_log (action, memory_id, source, detail, created_at) VALUES (?, ?, ?, ?, ?)",
			"write", mem.ID, mem.Source, "migrated from file store", mem.CreatedAt.Format("2006-01-02T15:04:05Z"))
		if mem.Phase == PhaseOrganized {
			sqliteStore.db.Exec("INSERT INTO activity_log (action, memory_id, source, detail, created_at) VALUES (?, ?, ?, ?, ?)",
				"upgrade", mem.ID, mem.Source, "migrated as organized", mem.UpdatedAt.Format("2006-01-02T15:04:05Z"))
		}
		result.Migrated++
	}

	return result, nil
}
