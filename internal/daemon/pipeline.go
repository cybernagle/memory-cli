package daemon

import (
	"context"
	"log"

	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/processor"
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

	tracker := processor.GlobalTracker
	tracker.Update(func(st *processor.ProcessStatus) {
		st.Running = true
		st.Phase = "extracting"
		st.Message = "Pipeline starting..."
	})
	tracker.Emit(processor.EventFromStatus("status"))

	result, err := t.Engine.Run(ctx, tracker)
	if err != nil {
		tracker.Update(func(st *processor.ProcessStatus) {
			st.Running = false
			st.Phase = "idle"
			st.LastError = err.Error()
		})
		return 0, err
	}

	// Log activity for heatmap
	if ss, ok := s.(*store.SqliteStore); ok {
		ss.LogActivity("pipeline", "", "daemon", "pipeline")
	}

	tracker.Update(func(st *processor.ProcessStatus) {
		st.Running = false
		st.Phase = "idle"
		st.Message = "Pipeline complete"
		st.Progress.Organized += result.Organized
		st.Progress.Processed += result.Processed
	})
	tracker.Emit(processor.EventFromStatus("done"))

	log.Printf("[pipeline] result: %d organized, %d processed", result.Organized, result.Processed)
	return result.Organized, nil
}

func inboxCount(s store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return 0, err
	}
	return len(memories), nil
}
