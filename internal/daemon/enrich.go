package daemon

import (
	"context"
	"log"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// EnrichTagsTask adds semantic concept/topic tags to memories using an LLM (glm-4.7).
// It complements the free keyword extractor (which only handles English/tech terms) by
// capturing abstract topics and Chinese concepts. Each memory is processed once, tracked
// via the "concept-tagged" marker so re-runs are idempotent.
type EnrichTagsTask struct {
	LLM   *llm.Client
	Store *store.SqliteStore
}

const (
	enrichMarker    = "concept-tagged"
	enrichPerTick   = 30 // memories enriched per tick (bounds LLM cost)
	enrichBatchSize = 10 // memories per LLM call
)

func (t *EnrichTagsTask) Name() string { return "enrich-tags" }

func (t *EnrichTagsTask) Run(s store.Store) (int, error) {
	if t.LLM == nil {
		return 0, nil
	}

	all, err := s.List(store.ListOptions{})
	if err != nil {
		return 0, err
	}

	// Pick memories not yet consumed by enrich-tags (any phase), up to the per-tick cap.
	var pending []*store.Memory
	for _, m := range all {
		if store.IsConsumedByMemory(m, "enrich-tags") {
			continue
		}
		pending = append(pending, m)
		if len(pending) >= enrichPerTick {
			break
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	log.Printf("[enrich-tags] %d memories to tag this tick", len(pending))

	enriched := 0
	for i := 0; i < len(pending); i += enrichBatchSize {
		end := i + enrichBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[i:end]

		contents := make([]string, len(batch))
		for j, m := range batch {
			contents[j] = m.Content
		}

		tagSets, err := t.LLM.ConceptTags(context.Background(), contents)
		if err != nil {
			log.Printf("[enrich-tags] llm error: %v", err)
			continue
		}

		for j, m := range batch {
			var tags []string
			if j < len(tagSets) {
				tags = tagSets[j]
			}
			if len(tags) > 0 {
				if _, err := s.Tag(m.ID, tags, nil); err != nil {
					log.Printf("[enrich-tags] tag %s: %v", m.ID, err)
					continue
				}
			}
			s.MarkConsumed(m.ID, "enrich-tags")
			enriched++
		}
	}

	return enriched, nil
}
