package daemon

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

const upgradeThreshold = 3

type UpgradeTask struct{}

func (t *UpgradeTask) Name() string { return "upgrade" }

func (t *UpgradeTask) Run(s *store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Type: store.ShortTerm})
	if err != nil {
		return 0, err
	}

	count := 0
	for _, mem := range memories {
		if mem.AccessCount >= upgradeThreshold {
			if err := s.Upgrade(mem.ID); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}
