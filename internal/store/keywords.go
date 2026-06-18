package store

import (
	"regexp"
	"sort"
	"strings"
)

// keywordTokenRe matches an ASCII word that starts with a letter and may contain
// internal separators (- . / +) between alphanumeric runs. This keeps meaningful
// multi-part names intact: "state-management", "memory-cli", "node.js", "v2.0".
// CJK characters are not matched — Chinese semantic tagging is handled by the LLM layer.
var keywordTokenRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]+(?:[-./+][A-Za-z0-9]+)*`)

// stopWords are common English function words that carry no semantic signal as tags.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true, "these": true,
	"those": true, "with": true, "for": true, "from": true, "your": true, "you": true,
	"are": true, "was": true, "were": true, "have": true, "has": true, "had": true,
	"not": true, "but": true, "and": true, "or": true, "can": true, "will": true,
	"just": true, "like": true, "then": true, "than": true, "into": true, "about": true,
	"what": true, "when": true, "how": true, "why": true, "which": true, "who": true,
	"use": true, "used": true, "using": true, "all": true, "any": true, "some": true,
	"new": true, "one": true, "two": true, "here": true, "there": true, "need": true,
	"want": true, "make": true, "made": true, "its": true, "it's": true, "they": true,
	"them": true, "their": true, "out": true, "our": true, "via": true, "per": true,
}

const maxKeywordTags = 6

// ExtractKeywords pulls high-precision ASCII/technical terms out of content for use as tags.
// It is deterministic, dependency-free, and instant. Returns lowercased, deduped keywords
// ranked by specificity (multi-part names first, then frequency, then length), capped at 6.
func ExtractKeywords(content string) []string {
	matches := keywordTokenRe.FindAllString(content, -1)
	if len(matches) == 0 {
		return nil
	}

	type cand struct {
		token  string
		freq   int
		multi  bool
		length int
	}
	counts := make(map[string]int)
	order := make(map[string]int) // first-seen order for stable tie-breaking
	seq := 0
	for _, m := range matches {
		lower := strings.ToLower(m)
		if len(lower) < 3 || stopWords[lower] {
			continue
		}
		if _, seen := order[lower]; !seen {
			order[lower] = seq
			seq++
		}
		counts[lower]++
	}

	cands := make([]cand, 0, len(counts))
	for tok, freq := range counts {
		cands = append(cands, cand{
			token:  tok,
			freq:   freq,
			multi:  strings.ContainsAny(tok, "-./+"),
			length: len(tok),
		})
	}

	sort.Slice(cands, func(i, j int) bool {
		// multi-part names (state-management, node.js) rank highest — they are the most specific.
		if cands[i].multi != cands[j].multi {
			return cands[i].multi
		}
		if cands[i].freq != cands[j].freq {
			return cands[i].freq > cands[j].freq
		}
		if cands[i].length != cands[j].length {
			return cands[i].length > cands[j].length
		}
		return order[cands[i].token] < order[cands[j].token]
	})

	if len(cands) > maxKeywordTags {
		cands = cands[:maxKeywordTags]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.token)
	}
	return out
}

// mergeTags appends extra tags to existing, skipping duplicates (case-insensitive),
// preserving the order of existing tags first.
func mergeTags(existing, extra []string) []string {
	seen := make(map[string]bool, len(existing)+len(extra))
	out := make([]string, 0, len(existing)+len(extra))
	for _, t := range existing {
		key := strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	for _, t := range extra {
		key := strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}
