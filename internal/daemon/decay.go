package daemon

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type DecayTask struct {
	Threshold time.Duration
}

func (t *DecayTask) Name() string { return "decay" }

func (t *DecayTask) Run(s store.Store) (int, error) {
	// Decay is a no-op: data is never deleted, only consolidated.
	// Old decay logic deleted organized memories with AccessCount=0 older
	// than threshold, which conflicts with the principle that total never decreases.
	return 0, nil
}
