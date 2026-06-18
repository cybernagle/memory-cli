package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"

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

	total := 0

	// Phase A: processed → organized
	n, err := t.consolidateProcessed(s)
	if err != nil {
		log.Printf("[consolidate-llm] Phase A error: %v", err)
	}
	total += n

	// Phase B: re-consolidate organized (1-2 categories per tick)
	n, err = t.reconsolidateOrganized(s)
	if err != nil {
		log.Printf("[consolidate-llm] Phase B error: %v", err)
	}
	total += n

	return total, nil
}

// Phase A: consolidate processed → organized
func (t *ConsolidateLLMTask) consolidateProcessed(s store.Store) (int, error) {
	processed, err := s.List(store.ListOptions{Phase: store.PhaseProcessed})
	if err != nil {
		return 0, err
	}

	var unconsolidated []*store.Memory
	for _, m := range processed {
		if !store.IsConsumedByMemory(m, "consolidate-llm") {
			unconsolidated = append(unconsolidated, m)
		}
	}

	if len(unconsolidated) < 2 {
		return 0, nil
	}

	log.Printf("[consolidate-llm] Phase A: %d unconsolidated processed memories", len(unconsolidated))

	// Code-based dedup: remove exact and near duplicates before LLM
	deduped := codeDedup(unconsolidated)
	removed := len(unconsolidated) - len(deduped)
	if removed > 0 {
		log.Printf("[consolidate-llm] Phase A: code dedup removed %d duplicates (%d → %d)", removed, len(unconsolidated), len(deduped))
	}

	groups := groupByProject(deduped)

	totalMerged := 0
	totalDeleted := 0

	for proj, memories := range groups {
		if proj == "(none)" || len(memories) < 2 {
			continue
		}

		merges := make([]llm.MergedMemory, len(memories))
		idMap := make(map[string]string) // index -> UUID
		for i, m := range memories {
			idx := fmt.Sprintf("%d", i+1)
			idMap[idx] = m.ID
			merges[i] = llm.MergedMemory{
				Content:    m.Content,
				Categories: []string{string(m.Category)},
				Tags:       m.Tags,
				Confidence: 0.8,
				SourceIDs:  []string{idx},
			}
		}

		const batchSize = 50
		for i := 0; i < len(merges); i += batchSize {
			end := i + batchSize
			if end > len(merges) {
				end = len(merges)
			}
			batch := merges[i:end]

			merged, err := t.LLM.Merge(context.Background(), llm.MergeRequest{Memories: batch})
			if err != nil {
				log.Printf("[consolidate-llm] merge error (project %s): %v", proj, err)
				continue
			}

			if len(merged) >= len(batch) {
				log.Printf("[consolidate-llm] project %s: no reduction (%d → %d), skip", proj, len(batch), len(merged))
				continue
			}

			// Resolve LLM index-based source_ids to actual UUIDs
			var sourceIDsToDelete []string
			for _, m := range merged {
				for _, sid := range m.SourceIDs {
					if uuid, ok := idMap[sid]; ok {
						sourceIDsToDelete = append(sourceIDsToDelete, uuid)
					}
				}
			}

			for _, m := range merged {
				mCat := store.CategoryKnowledge
				if len(m.Categories) > 0 {
					mCat = store.NormalizeCategory(store.Category(m.Categories[0]))
				}
				tags := m.Tags
				if len(tags) == 0 {
					tags = m.Categories
				}
				written, err := s.Write(m.Content, store.PhaseOrganized, mCat, "global", tags, "consolidate")
				if err != nil {
					log.Printf("[consolidate-llm] write error: %v", err)
					continue
				}
				setProjectOnMemory(t.Store, written.ID, proj)
				s.MarkConsumed(written.ID, "consolidate-llm")
				totalMerged++
			}

			for _, id := range sourceIDsToDelete {
				if err := s.Delete(id); err != nil {
					log.Printf("[consolidate-llm] delete processed %s: %v", id, err)
				} else {
					totalDeleted++
				}
			}
		}
	}

	log.Printf("[consolidate-llm] Phase A: merged %d organized, deleted %d processed", totalMerged, totalDeleted)
	return totalMerged, nil
}

// Phase B: re-consolidate organized memories (1-2 largest categories per tick)
func (t *ConsolidateLLMTask) reconsolidateOrganized(s store.Store) (int, error) {
	organized, err := s.List(store.ListOptions{Phase: store.PhaseOrganized})
	if err != nil {
		return 0, err
	}

	if len(organized) < 10 {
		return 0, nil
	}

	groups := groupByProject(organized)

	// Find top 2 largest projects that haven't been recently re-consolidated
	type catSize struct {
		cat  string
		mems []*store.Memory
	}
	var ranked []catSize
	for cat, mems := range groups {
		if cat == "(none)" || len(mems) < 5 {
			continue
		}
		// Skip projects where most memories are already consumed by consolidate-llm
		tagged := 0
		for _, m := range mems {
			if store.IsConsumedByMemory(m, "consolidate-llm") {
				tagged++
			}
		}
		if float64(tagged)/float64(len(mems)) > 0.8 {
			continue
		}
		ranked = append(ranked, catSize{cat, mems})
	}

	// Sort by size descending
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if len(ranked[j].mems) > len(ranked[i].mems) {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}

	// Process top 2
	limit := 2
	if len(ranked) < limit {
		limit = len(ranked)
	}

	totalMerged := 0

	for i := 0; i < limit; i++ {
		cat := ranked[i].cat
		memories := ranked[i].mems

		log.Printf("[consolidate-llm] Phase B: re-consolidating project '%s' (%d memories)", cat, len(memories))

		// Code dedup first
		deduped := codeDedup(memories)
		removed := len(memories) - len(deduped)
		if removed > 0 {
			log.Printf("[consolidate-llm] Phase B: code dedup removed %d duplicates in '%s'", removed, cat)
			// Delete exact duplicates
			seen := make(map[string]bool)
			for _, m := range memories {
				key := contentFingerprint(m.Content)
				if seen[key] {
					s.Delete(m.ID)
				}
				seen[key] = true
			}
			memories = deduped
		}

		if len(memories) < 3 {
			continue
		}

		// LLM merge
		merges := make([]llm.MergedMemory, len(memories))
		idMap := make(map[string]string)
		for j, m := range memories {
			idx := fmt.Sprintf("%d", j+1)
			idMap[idx] = m.ID
			merges[j] = llm.MergedMemory{
				Content:    m.Content,
				Categories: []string{string(m.Category)},
				Tags:       m.Tags,
				Confidence: 0.8,
				SourceIDs:  []string{idx},
			}
		}

		merged, err := t.LLM.Merge(context.Background(), llm.MergeRequest{Memories: merges})
		if err != nil {
			log.Printf("[consolidate-llm] Phase B merge error (%s): %v", cat, err)
			continue
		}

		if len(merged) >= len(memories) {
			log.Printf("[consolidate-llm] Phase B: '%s' no reduction (%d → %d)", cat, len(memories), len(merged))
			// Mark all consumed so we skip this project next time
			for _, m := range memories {
				s.MarkConsumed(m.ID, "consolidate-llm")
			}
			continue
		}

		// Resolve source IDs and delete originals
		var sourceIDsToDelete []string
		for _, m := range merged {
			for _, sid := range m.SourceIDs {
				if uuid, ok := idMap[sid]; ok {
					sourceIDsToDelete = append(sourceIDsToDelete, uuid)
				}
			}
		}

		for _, m := range merged {
			mCat := store.CategoryKnowledge
			if len(m.Categories) > 0 {
				mCat = store.NormalizeCategory(store.Category(m.Categories[0]))
			}
			tags := m.Tags
			if len(tags) == 0 {
				tags = m.Categories
			}
			written, err := s.Write(m.Content, store.PhaseOrganized, mCat, "global", tags, "consolidate")
			if err != nil {
				log.Printf("[consolidate-llm] Phase B write error: %v", err)
				continue
			}
			setProjectOnMemory(t.Store, written.ID, cat)
			s.MarkConsumed(written.ID, "consolidate-llm")
			totalMerged++
		}

		for _, id := range sourceIDsToDelete {
			if err := s.Delete(id); err != nil {
				log.Printf("[consolidate-llm] Phase B delete %s: %v", id, err)
			}
		}

		log.Printf("[consolidate-llm] Phase B: '%s' %d → %d (deleted %d)", cat, len(memories), len(merged), len(sourceIDsToDelete))
	}

	return totalMerged, nil
}

// codeDedup removes exact and near duplicates using content fingerprinting.
func codeDedup(memories []*store.Memory) []*store.Memory {
	seen := make(map[string]bool)
	var result []*store.Memory
	for _, m := range memories {
		fp := contentFingerprint(m.Content)
		if !seen[fp] {
			seen[fp] = true
			result = append(result, m)
		}
	}
	return result
}

// contentFingerprint creates a dedup key from content.
func contentFingerprint(content string) string {
	// Normalize: lowercase, collapse whitespace, strip punctuation
	s := strings.ToLower(content)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	// Collapse multiple spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	// Use first 200 chars as fingerprint
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func groupByCategory(memories []*store.Memory) map[string][]*store.Memory {
	groups := make(map[string][]*store.Memory)
	for _, m := range memories {
		cat := string(store.NormalizeCategory(m.Category))
		groups[cat] = append(groups[cat], m)
	}
	return groups
}

// groupByProject clusters memories by their Project field — the strongest relatedness signal
// (all memories from ~/Desktop/Code/makro belong together). Memories with no project land in
// a "(none)" bucket so they are still consolidable, just with a weaker signal.
func groupByProject(memories []*store.Memory) map[string][]*store.Memory {
	groups := make(map[string][]*store.Memory)
	for _, m := range memories {
		key := m.Project
		if key == "" {
			key = "(none)"
		}
		groups[key] = append(groups[key], m)
	}
	return groups
}

// setProjectOnMemory stamps the originating project onto a freshly-written merged memory.
// Write() has no project parameter, so we backfill it directly. No-op for the "(none)" bucket.
func setProjectOnMemory(s *store.SqliteStore, id, project string) {
	if project == "" || project == "(none)" {
		return
	}
	s.DB().Exec("UPDATE memories SET project = ? WHERE id = ?", project, id)
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
