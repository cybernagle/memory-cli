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
// This is the free (no-LLM) categorizer. It targets high-precision English AND Chinese cues
// (the conversation mix is bilingual). Chinese concept categorization is otherwise handled
// by the LLM enrichment layer.
var categoryRuleSet = mustCompileRules([]struct {
	cat     Category
	phrases []string
}{
	{CategoryPreferences, []string{
		"prefer", "prefers", "preferred", "preference", "preferences",
		"i like", "i dislike", "i hate", "i love", "i'd rather", "i'd prefer",
		"favorite", "favourite", "don't like", "always use", "never use", "i want",
		// Chinese preference cues
		"我喜欢", "我偏好", "我习惯", "我倾向", "我不喜欢", "我讨厌", "我习惯用",
		"更喜欢", "最好用", "建议用", "统一用", "不要用", "一直用",
	}},
	{CategoryDecisions, []string{
		"decided", "decision", "we should", "we must", "we will", "we agreed",
		"chose", "chosen", "going with", "going to use", "let's use", "let's go",
		"standardize", "standardise", "the rule is", "convention",
		// Chinese decision cues
		"决定", "决定用", "决定采用", "我们决定", "最终决定", "应该用", "必须用",
		"约定", "规范是", "采用", "选用", "切换到", "升级到", "迁移到",
	}},
	{CategoryLessons, []string{
		"learned", "learnt", "lesson", "mistake", "realized", "realised",
		"turns out", "gotcha", "pitfall", "takeaway", "note to self",
		"don't repeat", "beware", "root cause", "the problem was",
		// Chinese lesson cues
		"教训", "踩坑", "踩了坑", "坑爹", "原来是", "根因", "根本原因",
		"错误原因", "问题出在", "经验是", "总结一下",
	}},
	{CategoryReminders, []string{
		"remind", "remember to", "don't forget", "need to", "todo", "to-do",
		"follow up", "check later", "pending",
		// Chinese reminder cues
		"记得", "别忘了", "待办", "稍后", "之后要", "需要检查",
	}},
	{CategoryHabits, []string{
		"habit", "my routine", "daily routine", "every day", "every morning",
		"every night", "my workflow", "daily i",
		// Chinese habit cues
		"我的习惯", "每天", "日常流程", "工作流是",
	}},
	{CategorySkills, []string{
		"how to", "how do i", "how do you", "tutorial", "step by step",
		"technique", "learn to", "learning to",
		// Chinese skill cues
		"怎么用", "如何实现", "教程", "步骤", "方法是",
	}},
	{CategoryFeedback, []string{
		"feedback", "suggestion", "could improve", "issue with", "complaint",
		"critique", "bug report", "doesn't work", "broken",
		// Chinese feedback cues
		"建议", "反馈", "有问题", "不好用", "改进",
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
