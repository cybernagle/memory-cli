package daemon

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type DecayTask struct {
	Threshold time.Duration
}

func (t *DecayTask) Name() string { return "decay" }

func (t *DecayTask) Run(s *store.Store) (int, error) {
	threshold := t.Threshold
	if threshold == 0 {
		threshold = 30 * 24 * time.Hour
	}

	memories, err := s.List(store.ListOptions{Type: store.LongTerm})
	if err != nil {
		return 0, err
	}

	now := time.Now()
	count := 0
	for _, mem := range memories {
		if now.Sub(mem.CreatedAt) > threshold && mem.AccessCount == 0 {
			if err := s.Delete(mem.ID); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}
