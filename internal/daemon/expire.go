package daemon

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type ExpireTask struct{}

func (t *ExpireTask) Name() string { return "expire" }

func (t *ExpireTask) Run(s *store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Type: store.ShortTerm})
	if err != nil {
		return 0, err
	}

	now := time.Now()
	count := 0
	for _, mem := range memories {
		if mem.ExpiresAt != nil && now.After(*mem.ExpiresAt) {
			if err := s.Delete(mem.ID); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}
