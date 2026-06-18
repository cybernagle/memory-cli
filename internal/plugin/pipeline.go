package plugin

import (
	"context"
	"fmt"
	"log"

	"github.com/cybernagle/memory-cli/internal/processor"
	"github.com/cybernagle/memory-cli/internal/store"
)

// DataItemRouter persists DataItems. Set by the caller to avoid circular imports.
type DataItemRouter func(ctx context.Context, item DataItem) error

// PipelineEngine connects inbox → EntityResolver → Processors → Components.
type PipelineEngine struct {
	registry *Registry
	store    store.Store
	router   DataItemRouter
}

func NewPipelineEngine(registry *Registry, store store.Store, router DataItemRouter) *PipelineEngine {
	return &PipelineEngine{
		registry: registry,
		store:    store,
		router:   router,
	}
}

// Run reads inbox items not yet consumed by each processor, processes them, and marks consumption.
func (e *PipelineEngine) Run(ctx context.Context, tracker *processor.StatusTracker) (*PipelineResult, error) {
	totalResult := &PipelineResult{}
	resolver := NewCompositeResolver(e.registry.AllComponents())

	for _, proc := range e.registry.AllProcessors() {
		memories, err := e.store.ListUnconsumed(proc.Name())
		if err != nil {
			return nil, fmt.Errorf("list unconsumed for %s: %w", proc.Name(), err)
		}
		if len(memories) == 0 {
			continue
		}

		log.Printf("[pipeline] processor %s: %d unconsumed inbox memories", proc.Name(), len(memories))

		if tracker != nil {
			tracker.Update(func(st *processor.ProcessStatus) {
				st.Progress.TotalInbox = len(memories)
				st.Phase = "extracting"
				st.Message = fmt.Sprintf("Processor %s: %d items", proc.Name(), len(memories))
			})
			tracker.Emit(processor.EventFromStatus("status"))
		}

		items := make([]InboxItem, len(memories))
		for i, m := range memories {
			items[i] = InboxItem{
				ID:        m.ID,
				Content:   m.Content,
				SessionID: m.SessionID,
				Project:   m.Project,
				PromptID:  m.PromptID,
				Source:    m.Source,
				Tags:      m.Tags,
				CreatedAt: m.CreatedAt,
			}
		}

		input := ProcessInput{
			Items:      items,
			Components: resolver,
		}

		output, err := proc.Process(ctx, input)
		if err != nil {
			log.Printf("[pipeline] processor %s failed: %v", proc.Name(), err)
			totalResult.Errors++
			continue
		}

		log.Printf("[pipeline] processor %s: %d items → %d results",
			proc.Name(), len(items), len(output.Results))

		if tracker != nil {
			tracker.Update(func(st *processor.ProcessStatus) {
				st.Phase = "writing"
				st.Message = fmt.Sprintf("Writing %d results from %s", len(output.Results), proc.Name())
				st.Progress.Layer1Input = len(items)
				st.Progress.Layer1Output = len(output.Results)
			})
			tracker.Emit(processor.EventFromStatus("status"))
		}

		for _, item := range output.Results {
			if err := e.router(ctx, item); err != nil {
				log.Printf("[pipeline] route result: %v", err)
				totalResult.Errors++
			} else {
				totalResult.Organized++
			}
		}

		for _, id := range output.SourceIDs {
			if err := e.store.MarkConsumed(id, proc.Name()); err != nil {
				log.Printf("[pipeline] mark consumed %s for %s: %v", id, proc.Name(), err)
			} else {
				totalResult.Processed++
				// Also flip phase inbox→processed. MarkConsumed only sets the bitmask bit, leaving
				// phase='inbox' — which inflates the dashboard's inbox count with items that are
				// already consumed and will never be reprocessed. Aligning phase with the legacy
				// processor path keeps List(Phase:inbox) honest.
				_ = e.store.MarkProcessed(id)
			}
		}

		totalResult.ProcessorsRun++
	}

	if totalResult.ProcessorsRun == 0 {
		return &PipelineResult{Skipped: true, Reason: "no unconsumed inbox memories"}, nil
	}

	log.Printf("[pipeline] done: %d organized, %d consumed, %d errors",
		totalResult.Organized, totalResult.Processed, totalResult.Errors)
	return totalResult, nil
}

// CompositeResolver implements ComponentResolver by checking all registered components.
type CompositeResolver struct {
	components []Component
}

func NewCompositeResolver(components []Component) *CompositeResolver {
	return &CompositeResolver{components: components}
}

func (cr *CompositeResolver) Resolve(ctx context.Context, dataType DataType, mention string) (string, bool, error) {
	for _, c := range cr.components {
		if c.DataType() == dataType {
			id, ok, err := c.Resolve(ctx, mention)
			if err != nil {
				continue
			}
			if ok {
				return id, true, nil
			}
		}
	}
	return "", false, nil
}

type PipelineResult struct {
	Skipped       bool   `json:"skipped"`
	Reason        string `json:"reason,omitempty"`
	ProcessorsRun int    `json:"processors_run"`
	Organized     int    `json:"organized"`
	Processed     int    `json:"processed"`
	Errors        int    `json:"errors"`
}
