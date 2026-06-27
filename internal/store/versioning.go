package store

import (
	"regexp"
	"strings"
)

// versioning.go holds the version-tracking logic: detecting when a newly written memory is a
// newer version of an existing fact and marking the old one obsolete (superseded).
//
// Layering note (chaos point ③, re-evaluated 2026-06-27): the original ARCHITECTURE claim was
// that CheckAndSupersede "needs entity + predicate info the store layer doesn't have, so move
// it to a domain layer." That claim is FALSE — the judgment uses only content + category +
// project + phase + timestamps, all store-row fields. It's a text-overlap heuristic (≥3 shared
// CJK/ASCII terms), not an entity/predicate semantic judgment. So versioning stays in the store
// layer. This file just fixes an ORGANIZATION problem: the logic lived in entity_search.go
// (a search file) under a misleading home. Moving it here makes the codebase honest about what
// is search vs. what is versioning. No layer change, no behavior change.
//
// Trigger: CheckAndSupersede runs as a post-commit side-effect of IngestMemory (chaos point ②),
// so every write path triggers it, not just one manual caller.

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

// MarkSuperseded marks an old memory as superseded by a newer one. Sets metadata.superseded_by
// to the newer memory's ID; search ranking (sortByRecencyAndSupersede) then demotes it.
func (s *SqliteStore) MarkSuperseded(oldID, newID string) error {
	return s.UpdateMemoryMetadata(oldID, map[string]any{"superseded_by": newID})
}

// CheckAndSupersede checks if a newly written organized memory overlaps with existing
// organized memories (same project/category + shared frequent terms). If overlap found,
// marks the old ones as superseded. Returns the count newly superseded.
//
// Only runs for organized/processed memories (facts that can have versions); inbox/capture
// writes are raw events that never supersede. Idempotent: already-superseded memories are
// skipped, and a memory can't be older than itself.
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

// extractTermsFromContent extracts meaningful terms from a single memory's content. Used by
// CheckAndSupersede to measure overlap between two memories. Returns 2-4 char CJK prefixes
// plus ASCII words (≥2 chars, non-stop-word), deduped.
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

// ─── Term-extraction patterns (shared by versioning) ───

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
