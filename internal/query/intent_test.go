package query

import (
	"strings"
	"testing"
	"time"
)

func TestDetectTimeIntent(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"今天都干嘛了", true},
		{"昨天做了什么", true},
		{"前天的记录", true},
		{"上周的项目进度", true},
		{"这周完成了什么", true},
		{"近7天的活动", true},
		{"6月20号的记忆", true},
		{"6月20号到6月25号", true},
		{"2026-06-20", true},
		{"today", true},
		{"yesterday", true},
		{"last week", true},
		// Non-time queries.
		{"瑞福莱的合同金额", false},
		{"用户喜欢什么语言", false},
		{"memory cli 架构", false},
	}
	now := time.Now()
	_ = now
	for _, c := range cases {
		_, ok := DetectTimeIntent(c.q)
		if ok != c.want {
			t.Errorf("DetectTimeIntent(%q) = %v, want %v", c.q, ok, c.want)
		}
	}
}

func TestDetectAggregateIntent(t *testing.T) {
	yes := []string{"有多少合同", "有哪些企业", "什么公司", "列出所有项目", "几个客户", "哪些合同"}
	for _, q := range yes {
		if !DetectAggregateIntent(q) {
			t.Errorf("DetectAggregateIntent(%q) = false, want true", q)
		}
	}
	no := []string{"瑞福莱的合同", "用户偏好", "今天做了什么"}
	for _, q := range no {
		if DetectAggregateIntent(q) {
			t.Errorf("DetectAggregateIntent(%q) = true, want false", q)
		}
	}
}

func TestDetectRelationIntent(t *testing.T) {
	yes := []string{"瑞福莱和橘粒科技什么关系", "A与B如何关联", "甲跟乙有什么联系"}
	for _, q := range yes {
		if !DetectRelationIntent(q) {
			t.Errorf("DetectRelationIntent(%q) = false, want true", q)
		}
	}
	no := []string{"瑞福莱的合同", "今天做了什么"}
	for _, q := range no {
		if DetectRelationIntent(q) {
			t.Errorf("DetectRelationIntent(%q) = true, want false", q)
		}
	}
}

func TestSplitCJKKeywords(t *testing.T) {
	// A single CJK run → one 3-char-prefix token. Punctuation splits runs into multiple.
	got := SplitCJKKeywords("瑞福莱 合同 金额")
	if got == "" {
		t.Fatal("expected non-empty keywords")
	}
	if !strings.Contains(got, "OR") {
		t.Errorf("expected OR-joined keywords for space-separated input, got %q", got)
	}
	if !strings.Contains(got, "瑞福莱") {
		t.Errorf("expected 瑞福莱 token, got %q", got)
	}

	// ASCII preserved.
	got2 := SplitCJKKeywords("RSA 加密算法")
	if !strings.Contains(got2, "RSA") {
		t.Errorf("expected RSA keyword preserved, got %q", got2)
	}

	// Empty-ish input returns the question itself.
	empty := SplitCJKKeywords("的")
	if empty == "" {
		t.Error("expected fallback to question, got empty")
	}
}

func TestExtractSnippet(t *testing.T) {
	// Place the keyword well within a short-enough window so it lands in the output.
	pad := strings.Repeat("x", 50)
	content := pad + "瑞福莱合同" + pad
	snippet := ExtractSnippet(content, []string{"瑞福莱"}, 200)
	if !strings.Contains(snippet, "瑞福莱") {
		t.Errorf("snippet should contain the keyword, got %q", snippet)
	}
}

func TestSplitKeywordsForSnippet(t *testing.T) {
	got := SplitKeywordsForSnippet("RSA OR 瑞福莱 OR 合同")
	if len(got) != 3 {
		t.Errorf("expected 3 keywords, got %d: %v", len(got), got)
	}
	if got[0] != "rsa" {
		t.Errorf("expected lowercase 'rsa', got %q", got[0])
	}
}
