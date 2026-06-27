package store

import (
	"sort"
	"strings"
)

// SearchWithExpansion does a two-pass search: first search the user's query, then extract
// frequent terms from the results and do a second search with those terms. This expands
// the result set to include memories that don't contain the exact query keywords but are
// topically related (same project, same contract, same entities).
//
// Example: search "瑞福莱" → finds 23 memories. extractFrequentTerms finds "合同"(8x),
// "橘粒"(6x), "暖通"(5x). Second search with "合同 OR 橘粒 OR 暖通" finds the 6/22
// memory "张合同5万4确定下来了" which doesn't contain "瑞福莱" but is the latest contract.
//
// Results are deduped, sorted by created_at DESC (newest first), and superseded memories
// are pushed to the back.
func (s *SqliteStore) SearchWithExpansion(opts SearchOptions) ([]*Memory, error) {
	// Pass 1: initial search with user's query.
	firstResults, err := s.SearchLike(opts)
	if err != nil {
		return nil, err
	}

	// If no initial results, try raw LIKE on all phases.
	if len(firstResults) == 0 {
		return s.SearchLike(opts)
	}

	// Extract frequent terms from the initial results.
	terms := extractFrequentTerms(firstResults, 5)
	if len(terms) == 0 {
		// No expansion terms found — return initial results, sorted newest-first.
		sortByRecency(firstResults)
		return firstResults, nil
	}

	// Pass 2: search with the expansion terms.
	expandQuery := strings.Join(terms, " OR ")
	expandOpts := opts
	expandOpts.Query = expandQuery
	expandResults, err := s.SearchLike(expandOpts)
	if err != nil {
		expandResults = nil
	}

	// Merge: dedup by ID, initial results first (higher confidence), then expansion-only.
	seen := make(map[string]bool)
	var merged []*Memory
	for _, m := range firstResults {
		if !seen[m.ID] {
			seen[m.ID] = true
			merged = append(merged, m)
		}
	}
	for _, m := range expandResults {
		if !seen[m.ID] {
			seen[m.ID] = true
			merged = append(merged, m)
		}
	}

	// Sort by recency (newest first), superseded pushed to back.
	sortByRecencyAndSupersede(merged)
	return merged, nil
}

// extractFrequentTerms finds the most common meaningful substrings across a set of memories.
// It extracts CJK runs (2-6 chars) and ASCII tokens (2+ chars), counts their frequency,
// and returns the top N terms that appear in at least 30% of the memories (minimum 2).
//
// This is NOT entity recognition — it's co-occurrence statistics. Terms that appear across
// many of the initial search results are likely topically significant entities/concepts that
// can be used to expand the search to related memories.
func extractFrequentTerms(memories []*Memory, topN int) []string {
	if len(memories) < 2 {
		return nil
	}

	// Count term frequency across memories. Each term is counted once per memory (not per
	// occurrence within a memory) to avoid one long memory dominating.
	termMemCount := make(map[string]int)
	for _, m := range memories {
		content := strings.ToLower(m.Content)
		seenInThisMem := make(map[string]bool)

		// Extract CJK runs and their short prefixes.
		for _, m := range cjkTermRe.FindAllString(content, -1) {
			runes := []rune(m)
			// Take 2-3 char prefixes of each CJK run.
			for n := 2; n <= 3 && n <= len(runes); n++ {
				term := string(runes[:n])
				if !seenInThisMem[term] {
					seenInThisMem[term] = true
					termMemCount[term]++
				}
			}
		}

		// Extract ASCII words (2+ chars).
		for _, w := range asciiWordRe.FindAllString(content, -1) {
			if len(w) >= 2 && !isStopWord(w) && !seenInThisMem[w] {
				seenInThisMem[w] = true
				termMemCount[w]++
			}
		}
	}

	// Threshold: term must appear in at least 50% of memories (min 3). Higher threshold
	// means only terms that are truly characteristic of the result set get used for expansion.
	threshold := len(memories) / 2
	if threshold < 3 {
		threshold = 3
	}

	// Filter and sort by frequency.
	type termFreq struct {
		term string
		freq int
	}
	var candidates []termFreq
	for term, freq := range termMemCount {
		if freq < threshold {
			continue
		}
		// Skip pure numbers (noise like "000", "15", "2026").
		isAllDigit := true
		for _, r := range term {
			if r < '0' || r > '9' {
				isAllDigit = false
				break
			}
		}
		if isAllDigit {
			continue
		}
		candidates = append(candidates, termFreq{term, freq})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].freq > candidates[j].freq })

	// Take top N.
	if len(candidates) > topN {
		candidates = candidates[:topN]
	}

	var out []string
	for _, c := range candidates {
		out = append(out, c.term)
	}
	return out
}

// sortByRecency sorts memories by created_at descending (newest first).
func sortByRecency(memories []*Memory) {
	sort.SliceStable(memories, func(i, j int) bool {
		return memories[i].CreatedAt.After(memories[j].CreatedAt)
	})
}

// sortByRecencyAndSupersede sorts newest first, with superseded memories pushed to the back.
func sortByRecencyAndSupersede(memories []*Memory) {
	sort.SliceStable(memories, func(i, j int) bool {
		iSuperseded := IsSuperseded(memories[i])
		jSuperseded := IsSuperseded(memories[j])
		if iSuperseded != jSuperseded {
			return !iSuperseded // non-superseded first
		}
		return memories[i].CreatedAt.After(memories[j].CreatedAt)
	})
}

