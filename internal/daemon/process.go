package daemon

import (
	"context"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/processor"
	"github.com/cybernagle/memory-cli/internal/store"
)

// ProcessTask runs the LLM-powered extraction+merge pipeline on inbox memories.
type ProcessTask struct {
	Store     *store.SqliteStore
	LLM       *llm.Client
	Threshold int
}

func (t *ProcessTask) Name() string { return "process" }

func (t *ProcessTask) Run(s store.Store) (int, error) {
	if t.Store == nil || t.LLM == nil {
		return 0, nil
	}

	cfg := processor.Config{
		InboxThreshold: t.Threshold,
	}
	if cfg.InboxThreshold <= 0 {
		cfg.InboxThreshold = 100
	}

	p := processor.New(t.Store, t.LLM, cfg)
	result, err := p.ProcessInbox(context.Background())
	if err != nil {
		return 0, err
	}
	if result.Skipped {
		return 0, nil
	}
	return result.Organized, nil
}
