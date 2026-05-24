package daemon

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

type ExpireTask struct{}

func (t *ExpireTask) Name() string { return "expire" }

func (t *ExpireTask) Run(s store.Store) (int, error) {
	// Expire is a no-op: inbox data is never deleted, only marked as processed.
	return 0, nil
}
