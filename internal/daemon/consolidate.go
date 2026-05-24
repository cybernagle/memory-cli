package daemon

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

type ConsolidateTask struct{}

func (t *ConsolidateTask) Name() string { return "consolidate" }

func (t *ConsolidateTask) Run(s store.Store) (int, error) {
	// No-op: dedup is handled during ingest via content hash.
	// Old logic deleted duplicate content prefixes, which reduced total count.
	return 0, nil
}
