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
	// content prefix -> best memory (highest access count)
	type best struct {
		id          string
		accessCount int
	}
	seen := make(map[string]*best)

	for _, mem := range memories {
		prefix := strings.ToLower(strings.TrimSpace(mem.Content))
		if len(prefix) > 100 {
			prefix = prefix[:100]
		}
		if existing, dup := seen[prefix]; dup {
			if mem.AccessCount >= existing.accessCount {
				// current is better or equal — delete the old one
				if err := s.Delete(existing.id); err != nil {
					continue
				}
				count++
				seen[prefix] = &best{id: mem.ID, accessCount: mem.AccessCount}
			} else {
				// existing is better — delete current
				if err := s.Delete(mem.ID); err != nil {
					continue
				}
				count++
				// don't update seen — existing stays the best
			}
		} else {
			seen[prefix] = &best{id: mem.ID, accessCount: mem.AccessCount}
		}
	}
	return count, nil
}
