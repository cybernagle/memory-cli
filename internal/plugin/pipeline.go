package plugin

import (
	"context"
	"fmt"
	"log"

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

// Run reads unprocessed inbox, routes to processors, writes results via components.
func (e *PipelineEngine) Run(ctx context.Context) (*PipelineResult, error) {
	memories, err := e.store.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	if len(memories) == 0 {
		return &PipelineResult{Skipped: true, Reason: "no inbox memories"}, nil
	}

	log.Printf("[pipeline] starting: %d inbox memories", len(memories))

	items := make([]InboxItem, len(memories))
	for i, m := range memories {
		items[i] = InboxItem{
			ID:        m.ID,
			Content:   m.Content,
			SessionID: m.SessionID,
			Source:    m.Source,
			Tags:      m.Tags,
			CreatedAt: m.CreatedAt,
		}
	}

	// Build ComponentResolver from all registered components
	resolver := NewCompositeResolver(e.registry.AllComponents())

	totalResult := &PipelineResult{}
	for _, proc := range e.registry.AllProcessors() {
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

		for _, item := range output.Results {
			if err := e.router(ctx, item); err != nil {
				log.Printf("[pipeline] route result: %v", err)
				totalResult.Errors++
			} else {
				totalResult.Organized++
			}
		}

		for _, id := range output.SourceIDs {
			if err := e.store.MarkProcessed(id); err != nil {
				log.Printf("[pipeline] mark processed %s: %v", id, err)
			} else {
				totalResult.Processed++
			}
		}

		totalResult.ProcessorsRun++
	}

	log.Printf("[pipeline] done: %d organized, %d processed, %d errors",
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
