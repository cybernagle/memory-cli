package daemon

import (
	"context"

	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/store"
)

type PipelineTask struct {
	Engine    *plugin.PipelineEngine
	Threshold int
}

func (t *PipelineTask) Name() string { return "pipeline" }

func (t *PipelineTask) Run(s store.Store) (int, error) {
	ctx := context.Background()
	if t.Threshold > 0 {
		count, err := inboxCount(s)
		if err != nil {
			return 0, err
		}
		if count < t.Threshold {
			return 0, nil
		}
	}
	result, err := t.Engine.Run(ctx)
	if err != nil {
		return 0, err
	}
	return result.Organized, nil
}

func inboxCount(s store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return 0, err
	}
	return len(memories), nil
}
