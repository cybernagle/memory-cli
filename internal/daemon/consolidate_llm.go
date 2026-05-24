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

	// Only consolidate newly added organized memories since last run
	if len(organized) <= last || len(organized)-last < 5 {
		return 0, nil
	}

	// Process only the delta (new memories since last consolidation)
	newMemories := organized[last:]
	log.Printf("[consolidate-llm] consolidating %d new organized memories (total %d, was %d)", len(newMemories), len(organized), last)

	// Group by category — only merge within same category
	groups := make(map[store.Category][]*store.Memory)
	for _, m := range newMemories {
		cat := m.Category
		if cat == "" {
			cat = store.CategoryKnowledge
		}
		groups[cat] = append(groups[cat], m)
	}

	totalCreated := 0

	for cat, memories := range groups {
		if len(memories) < 2 {
			continue
		}

		log.Printf("[consolidate-llm] category %s: %d memories", cat, len(memories))

		merges := make([]llm.MergedMemory, len(memories))
		for i, m := range memories {
			merges[i] = llm.MergedMemory{
				Content:    m.Content,
				Categories: []string{string(m.Category)},
				Tags:       m.Tags,
				Confidence: 0.8,
				SourceIDs:  []string{m.ID},
			}
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
				continue
			}

			// Only write if LLM actually reduced count
			if len(merged) >= len(batch) {
				log.Printf("[consolidate-llm] category %s: no reduction (%d → %d), skip", cat, len(batch), len(merged))
				continue
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
				totalCreated++
			}
		}
	}

	t.mu.Lock()
	t.lastCount = len(organized)
	t.mu.Unlock()

	log.Printf("[consolidate-llm] processed %d new → created %d summaries", len(newMemories), totalCreated)
	return totalCreated, nil
}
