package store

import (
	"regexp"
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

// IsSuperseded checks if a memory has been marked as superseded by a newer version.
func IsSuperseded(m *Memory) bool {
	if m.Metadata == nil {
		return false
	}
	if v, ok := m.Metadata["superseded_by"]; ok && v != nil && v != "" {
		return true
	}
	if v, ok := m.Metadata["superseded"]; ok {
		if b, ok2 := v.(bool); ok2 && b {
			return true
		}
	}
	return false
}

// MarkSuperseded marks an old memory as superseded by a newer one.
func (s *SqliteStore) MarkSuperseded(oldID, newID string) error {
	return s.UpdateMemoryMetadata(oldID, map[string]any{"superseded_by": newID})
}

// CheckAndSupersede checks if a newly written organized memory overlaps with existing
// organized memories (same project/category + shared frequent terms). If overlap found,
// marks the old ones as superseded.
func (s *SqliteStore) CheckAndSupersede(newMem *Memory) int {
	if newMem.Phase != PhaseOrganized && newMem.Phase != PhaseProcessed {
		return 0
	}

	// Get existing organized memories in the same category+project.
	opts := ListOptions{
		Phase:    newMem.Phase,
		Category: newMem.Category,
	}
	if newMem.Project != "" {
		opts.Project = newMem.Project
	}
	existing, err := s.List(opts)
	if err != nil || len(existing) == 0 {
		return 0
	}

	// Extract terms from the new memory.
	newTerms := extractTermsFromContent(newMem.Content)
	if len(newTerms) < 2 {
		return 0
	}
	newTermSet := make(map[string]bool)
	for _, t := range newTerms {
		newTermSet[t] = true
	}

	// Check each existing memory for overlap.
	superseded := 0
	for _, old := range existing {
		if old.ID == newMem.ID {
			continue
		}
		if old.CreatedAt.After(newMem.CreatedAt) || old.CreatedAt.Equal(newMem.CreatedAt) {
			continue // old is actually newer or same — skip
		}
		if IsSuperseded(old) {
			continue // already superseded
		}

		oldTerms := extractTermsFromContent(old.Content)
		shared := 0
		for _, t := range oldTerms {
			if newTermSet[t] {
				shared++
			}
		}
		// If they share ≥3 significant terms, the new one likely supersedes the old.
		if shared >= 3 {
			s.MarkSuperseded(old.ID, newMem.ID)
			superseded++
		}
	}
	return superseded
}

// extractTermsFromContent extracts meaningful terms from a single memory's content.
func extractTermsFromContent(content string) []string {
	content = strings.ToLower(content)
	var terms []string
	seen := make(map[string]bool)

	for _, m := range cjkTermRe.FindAllString(content, -1) {
		runes := []rune(m)
		for n := 2; n <= 4 && n <= len(runes); n++ {
			term := string(runes[:n])
			if !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	for _, w := range asciiWordRe.FindAllString(content, -1) {
		if len(w) >= 2 && !isStopWord(w) && !seen[w] {
			seen[w] = true
			terms = append(terms, w)
		}
	}
	return terms
}

// ─── Regex patterns ───

var (
	cjkTermRe   = regexp.MustCompile(`[\x{4e00}-\x{9fff}]+`)
	asciiWordRe = regexp.MustCompile(`[a-z0-9][a-z0-9._-]+`)
)

// isStopWord filters common terms that don't carry topical signal.
func isStopWord(w string) bool {
	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "from": true,
		"this": true, "that": true, "are": true, "was": true, "were": true,
		"has": true, "have": true, "not": true, "but": true, "all": true,
		"can": true, "will": true, "been": true, "into": true, "about": true,
		"using": true, "based": true, "which": true, "would": true, "should": true,
		"http": true, "https": true, "www": true, "com": true, "org": true,
		"true": true, "false": true, "null": true, "none": true,
	}
	return stopWords[w]
}
