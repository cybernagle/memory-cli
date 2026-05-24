package daemon

import (
	"context"
	"log"
	"sync"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

type ConsolidateLLMTask struct {
	Store *store.SqliteStore
	LLM   *llm.Client

	mu        sync.Mutex
	lastCount int // organized count after last consolidation
}

func (t *ConsolidateLLMTask) Name() string { return "consolidate-llm" }

func (t *ConsolidateLLMTask) Run(s store.Store) (int, error) {
	if t.LLM == nil {
		return 0, nil
	}

	organized, err := s.List(store.ListOptions{Phase: store.PhaseOrganized})
	if err != nil {
		return 0, err
	}

	t.mu.Lock()
	last := t.lastCount
	t.mu.Unlock()

	// Skip if organized count hasn't grown since last consolidation
	if len(organized) <= last || len(organized) < 5 {
		return 0, nil
	}

	log.Printf("[consolidate-llm] consolidating %d organized memories (was %d)", len(organized), last)

	// Group by category — only merge within same category
	groups := make(map[store.Category][]*store.Memory)
	for _, m := range organized {
		cat := m.Category
		if cat == "" {
			cat = store.CategoryKnowledge
		}
		groups[cat] = append(groups[cat], m)
	}

	totalBefore := len(organized)
	totalAfter := 0
	totalDeleted := 0

	for cat, memories := range groups {
		if len(memories) < 2 {
			// Not enough to merge, keep as-is
			totalAfter += len(memories)
			continue
		}

		log.Printf("[consolidate-llm] category %s: %d memories", cat, len(memories))

		merges := make([]llm.MergedMemory, len(memories))
		idMap := make(map[string]string, len(memories))
		for i, m := range memories {
			merges[i] = llm.MergedMemory{
				Content:    m.Content,
				Categories: []string{string(m.Category)},
				Tags:       m.Tags,
				Confidence: 0.8,
				SourceIDs:  []string{string(rune('0' + i))},
			}
			idMap[string(rune('0'+i))] = m.ID
		}

		const batchSize = 100
		for i := 0; i < len(merges); i += batchSize {
			end := i + batchSize
			if end > len(merges) {
				end = len(merges)
			}
			batch := merges[i:end]

			merged, err := t.LLM.Merge(context.Background(), llm.MergeRequest{Memories: batch})
			if err != nil {
				log.Printf("[consolidate-llm] merge error (category %s): %v", cat, err)
				totalAfter += len(batch)
				continue
			}

			// If LLM didn't reduce count, skip — keep originals
			if len(merged) >= len(batch) {
				log.Printf("[consolidate-llm] category %s: no reduction (%d → %d), keeping originals", cat, len(batch), len(merged))
				totalAfter += len(batch)
				continue
			}

			for _, src := range batch {
				if id, ok := idMap[src.SourceIDs[0]]; ok {
					s.Delete(id)
					totalDeleted++
				}
			}

			for _, m := range merged {
				mCat := store.CategoryKnowledge
				if len(m.Categories) > 0 {
					mCat = store.Category(m.Categories[0])
				}
				tags := m.Tags
				if len(tags) == 0 {
					tags = m.Categories
				}
				mem, err := s.Write(m.Content, store.PhaseOrganized, mCat, "global", tags, "consolidate")
				if err != nil {
					log.Printf("[consolidate-llm] write error: %v", err)
					continue
				}
				_ = mem
				totalAfter++
			}
		}
	}

	t.mu.Lock()
	t.lastCount = totalAfter
	t.mu.Unlock()

	log.Printf("[consolidate-llm] %d → %d (deleted %d)", totalBefore, totalAfter, totalDeleted)
	return totalDeleted, nil
}
