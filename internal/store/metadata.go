package store

import (
	"encoding/json"
)

// UpdateMemoryMetadata merges the given metadata patch into a memory's existing metadata.
// Existing keys are overwritten by the patch; keys not in the patch are preserved.
// This is the atomic state-machine update path for proposals (status, accepted_at, etc.)
// and profile evidence. SQLite-only.
func (s *SqliteStore) UpdateMemoryMetadata(id string, patch map[string]any) error {
	// Load current metadata, merge, write back. Two queries but simple and correct.
	var currentJSON string
	err := s.db.QueryRow("SELECT metadata FROM memories WHERE id = ?", id).Scan(&currentJSON)
	if err != nil {
		return err
	}

	var current map[string]any
	if currentJSON != "" && currentJSON != "{}" {
		json.Unmarshal([]byte(currentJSON), &current)
	}
	if current == nil {
		current = make(map[string]any)
	}
	// Merge patch into current (patch wins on conflict).
	for k, v := range patch {
		current[k] = v
	}

	merged, _ := json.Marshal(current)
	_, err = s.db.Exec("UPDATE memories SET metadata = ? WHERE id = ?", string(merged), id)
	return err
}
