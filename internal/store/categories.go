package store

import (
	"regexp"
	"strings"
)

// categoryRuleSet maps a category to a single compiled regex that matches any of its
// cue phrases on word boundaries (case-insensitive). Word boundaries prevent substring
// false positives like "routine" matching inside "goroutine". Checked in order; first
// match wins.
//
// This is the free (no-LLM) categorizer. It targets high-precision English cues.
// Chinese concept categorization is handled by the LLM enrichment layer (Phase 2).
var categoryRuleSet = mustCompileRules([]struct {
	cat     Category
	phrases []string
}{
	{CategoryPreferences, []string{
		"prefer", "prefers", "preferred", "preference", "preferences",
		"i like", "i dislike", "i hate", "i love", "i'd rather", "i'd prefer",
		"favorite", "favourite", "don't like", "always use", "never use", "i want",
	}},
	{CategoryDecisions, []string{
		"decided", "decision", "we should", "we must", "we will", "we agreed",
		"chose", "chosen", "going with", "going to use", "let's use", "let's go",
		"standardize", "standardise", "the rule is", "convention",
	}},
	{CategoryLessons, []string{
		"learned", "learnt", "lesson", "mistake", "realized", "realised",
		"turns out", "gotcha", "pitfall", "takeaway", "note to self",
		"don't repeat", "beware",
	}},
	{CategoryReminders, []string{
		"remind", "remember to", "don't forget", "need to", "todo", "to-do",
		"follow up", "check later", "pending",
	}},
	{CategoryHabits, []string{
		"habit", "my routine", "daily routine", "every day", "every morning",
		"every night", "my workflow", "daily i",
	}},
	{CategorySkills, []string{
		"how to", "how do i", "how do you", "tutorial", "step by step",
		"technique", "learn to", "learning to",
	}},
	{CategoryFeedback, []string{
		"feedback", "suggestion", "could improve", "issue with", "complaint",
		"critique",
	}},
})

func mustCompileRules(rules []struct {
	cat     Category
	phrases []string
}) []struct {
	cat   Category
	regex *regexp.Regexp
} {
	out := make([]struct {
		cat   Category
		regex *regexp.Regexp
	}, 0, len(rules))
	for _, r := range rules {
		parts := make([]string, len(r.phrases))
		for i, p := range r.phrases {
			parts[i] = `\b` + regexp.QuoteMeta(p) + `\b`
		}
		out = append(out, struct {
			cat   Category
			regex *regexp.Regexp
		}{cat: r.cat, regex: regexp.MustCompile("(?i)" + strings.Join(parts, "|"))})
	}
	return out
}

// CategorizeByKeywords assigns a category to content based on high-precision word-boundary
// cues. Returns CategoryInbox when no signal is found. See CategorizeContent for the
// knowledge-fallback variant used at write time.
func CategorizeByKeywords(content string) Category {
	for _, rule := range categoryRuleSet {
		if rule.regex.MatchString(content) {
			return rule.cat
		}
	}
	return CategoryInbox
}

// CategorizeContent is the entry-point categorizer used at write time: it applies the
// keyword rules, and falls back to knowledge when the content looks technical (has
// extractable keywords) but matched no specific rule. Truly signal-less content stays inbox.
func CategorizeContent(content string) Category {
	if cat := CategorizeByKeywords(content); cat != CategoryInbox {
		return cat
	}
	if len(ExtractKeywords(content)) > 0 {
		return CategoryKnowledge
	}
	return CategoryInbox
}
