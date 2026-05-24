package daemon

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

type UpgradeTask struct {
	Threshold int
}

func (t *UpgradeTask) Name() string { return "upgrade" }

func (t *UpgradeTask) Run(s store.Store) (int, error) {
	threshold := t.Threshold
	if threshold == 0 {
		threshold = 3
	}

	memories, err := s.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return 0, err
	}

	count := 0
	for _, mem := range memories {
		if mem.AccessCount >= threshold {
			if err := s.Upgrade(mem.ID); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}
