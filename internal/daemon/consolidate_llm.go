package daemon

import (
	"context"
	"log"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

type ConsolidateLLMTask struct {
	Store *store.SqliteStore
	LLM   *llm.Client
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
	if len(organized) < 50 {
		return 0, nil
	}

	log.Printf("[consolidate-llm] consolidating %d organized memories", len(organized))

	// Build merge input from all organized memories
	merges := make([]llm.MergedMemory, len(organized))
	idMap := make(map[string]string, len(organized)) // index string -> memory ID
	for i, m := range organized {
		merges[i] = llm.MergedMemory{
			Content:    m.Content,
			Categories: []string{string(m.Category)},
			Tags:       m.Tags,
			Confidence: 0.8,
			SourceIDs:  []string{string(rune('0' + i))},
		}
		idMap[string(rune('0'+i))] = m.ID
	}

	// Merge in batches of 100
	const batchSize = 100
	totalBefore := len(organized)
	totalAfter := 0
	totalDeleted := 0

	for i := 0; i < len(merges); i += batchSize {
		end := i + batchSize
		if end > len(merges) {
			end = len(merges)
		}
		batch := merges[i:end]

		merged, err := t.LLM.Merge(context.Background(), llm.MergeRequest{Memories: batch})
		if err != nil {
			log.Printf("[consolidate-llm] merge batch error: %v", err)
			continue
		}

		// Delete all source memories in this batch
		for _, src := range batch {
			if id, ok := idMap[src.SourceIDs[0]]; ok {
				s.Delete(id)
				totalDeleted++
			}
		}

		// Write consolidated memories
		for _, m := range merged {
			cat := store.CategoryKnowledge
			if len(m.Categories) > 0 {
				cat = store.Category(m.Categories[0])
			}
			tags := m.Tags
			if len(tags) == 0 {
				tags = m.Categories
			}
			mem, err := s.Write(m.Content, store.PhaseOrganized, cat, "global", tags, "consolidate")
			if err != nil {
				log.Printf("[consolidate-llm] write error: %v", err)
				continue
			}
			_ = mem
			totalAfter++
		}
	}

	log.Printf("[consolidate-llm] %d → %d (deleted %d)", totalBefore, totalAfter, totalDeleted)
	return totalDeleted, nil
}
