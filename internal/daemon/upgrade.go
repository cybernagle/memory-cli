package daemon

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

type UpgradeTask struct {
	Threshold int
}

func (t *UpgradeTask) Name() string { return "upgrade" }

func (t *UpgradeTask) Run(s store.Store) (int, error) {
	// No-op: inbox → organized must go through LLM pipeline (fact-processor).
	// Old logic upgraded based on access count, bypassing LLM extraction.
	return 0, nil
}
