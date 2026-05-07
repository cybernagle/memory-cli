package daemon

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

const decayThreshold = 30 * 24 * time.Hour

type DecayTask struct{}

func (t *DecayTask) Name() string { return "decay" }

func (t *DecayTask) Run(s *store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Type: store.LongTerm})
	if err != nil {
		return 0, err
	}

	now := time.Now()
	count := 0
	for _, mem := range memories {
		if now.Sub(mem.UpdatedAt) > decayThreshold && mem.AccessCount == 0 {
			if err := s.Delete(mem.ID); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}
