package store

import (
	"strings"
	"testing"
)

func TestExtractKeywordsEnglish(t *testing.T) {
	got := ExtractKeywords("User prefers dark mode with state-management in react and golang")
	// multi-part name ranks first
	if len(got) == 0 {
		t.Fatal("expected keywords, got none")
	}
	if !contains(got, "state-management") {
		t.Errorf("missing state-management in %v", got)
	}
	if !contains(got, "react") {
		t.Errorf("missing react in %v", got)
	}
	if !contains(got, "golang") {
		t.Errorf("missing golang in %v", got)
	}
	// stopwords must not appear
	for _, kw := range got {
		if stopWords[kw] {
			t.Errorf("stopword %q leaked into keywords", kw)
		}
	}
	// never exceeds cap
	if len(got) > maxKeywordTags {
		t.Errorf("got %d keywords, max %d", len(got), maxKeywordTags)
	}
}

func TestExtractKeywordsChineseOnlyYieldsNoNoise(t *testing.T) {
	// Pure Chinese content: free layer extracts no ASCII tokens (Chinese semantics = Phase 2 LLM).
	// Must not crash and must not produce garbage fragments.
	got := ExtractKeywords("如何保证合同上的章就是公司章而不是伪造的")
	for _, kw := range got {
		// any returned token must be a clean ASCII word, never a Chinese fragment
		for _, r := range kw {
			if r >= 0x4e00 { // CJK
				t.Errorf("Chinese fragment %q leaked into keywords (free layer must not tokenize CJK)", kw)
			}
		}
	}
	// mixed content keeps the English/tech term
	got = ExtractKeywords("前端 message 需要做状态管理，参考 react 的 useReducer")
	if !contains(got, "message") {
		t.Errorf("expected message in %v", got)
	}
}

func TestExtractKeywordsDedupAndNoiseFilter(t *testing.T) {
	content := "react react React the the the 123 456 v2.0"
	got := ExtractKeywords(content)
	// no duplicates (case-insensitive)
	seen := map[string]bool{}
	for _, kw := range got {
		k := strings.ToLower(kw)
		if seen[k] {
			t.Errorf("duplicate keyword %q", kw)
		}
		seen[k] = true
	}
	// pure numbers filtered
	if contains(got, "123") || contains(got, "456") {
		t.Errorf("pure number leaked into keywords: %v", got)
	}
	// version token kept
	if !contains(got, "v2.0") {
		t.Errorf("expected version token v2.0 in %v", got)
	}
}

func TestMergeTagsDedup(t *testing.T) {
	got := mergeTags([]string{"Claude", "react"}, []string{"react", "golang", ""})
	if !contains(got, "Claude") || !contains(got, "react") || !contains(got, "golang") {
		t.Errorf("merge missing tags: %v", got)
	}
	// react must appear once (case-insensitive dedup)
	count := 0
	for _, tg := range got {
		if strings.EqualFold(tg, "react") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("react appeared %d times, want 1: %v", count, got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
