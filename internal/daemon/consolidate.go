package daemon

import (
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
)

type ConsolidateTask struct{}

func (t *ConsolidateTask) Name() string { return "consolidate" }

func (t *ConsolidateTask) Run(s *store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{})
	if err != nil {
		return 0, err
	}

	count := 0
	seen := make(map[string]string) // content prefix -> memory ID

	for _, mem := range memories {
		prefix := strings.ToLower(strings.TrimSpace(mem.Content))
		if len(prefix) > 100 {
			prefix = prefix[:100]
		}
		if existingID, dup := seen[prefix]; dup {
			if err := s.Delete(existingID); err != nil {
				continue
			}
			count++
		}
		seen[prefix] = mem.ID
	}
	return count, nil
}
