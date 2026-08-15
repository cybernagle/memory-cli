// Package query holds the canonical, shared query-understanding helpers used by both the
// dashboard (HTTP) and MCP (stdio) ask/search paths.
//
// History: these helpers previously lived duplicated in two places — the rich versions in
// internal/dashboard/handlers.go + ask_workflow.go, and stripped-down *Standalone copies in
// internal/mcp/server.go. The MCP copies had drifted behind (e.g. detectTimeIntentStandalone
// only handled 今天/昨天 while the dashboard version handled 近N天/上周/这周/前天/月日/日期范围;
// the MCP keyword-extract prompt was weaker). That drift meant the same question got different
// answers depending on which entry point (dashboard vs MCP) handled it. This package is the
// single source of truth; both entry points call it.
package query

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
)

// DateRange is a resolved time-intent range (today / last week / 6月20号 / 2026-06-20 / …).
type DateRange struct {
	From  time.Time
	To    time.Time
	Label string
}

// DetectTimeIntent checks whether a question is time-scoped and, if so, returns the resolved
// date range. This is the canonical version (merged from dashboard's detectTimeIntent — the
// richer of the two former copies). Handles: 近N天, 上周/last week, 这周/本周/this week, 今天/today,
// 昨天/yesterday, 前天, N月M号~N月M号 ranges, N月M号, and YYYY-MM-DD.
func DetectTimeIntent(question string) (*DateRange, bool) {
	now := time.Now()
	loc := now.Location()
	lower := strings.ToLower(question)

	if m := regexp.MustCompile(`(?:近|最近)\s*(\d+)\s*天`).FindStringSubmatch(question); m != nil {
		n, _ := strconv.Atoi(m[1])
		return &DateRange{From: now.AddDate(0, 0, -n), To: now, Label: fmt.Sprintf("近%d天", n)}, true
	}
	if strings.Contains(question, "上周") || strings.Contains(lower, "last week") {
		thisMonday := now.AddDate(0, 0, -(int(now.Weekday())+6)%7)
		lastMonday := thisMonday.AddDate(0, 0, -7)
		lastSunday := lastMonday.AddDate(0, 0, 6)
		return &DateRange{From: lastMonday, To: lastSunday, Label: fmt.Sprintf("上周(%s到%s)", lastMonday.Format("1月2日"), lastSunday.Format("1月2日"))}, true
	}
	if strings.Contains(question, "这周") || strings.Contains(question, "本周") || strings.Contains(lower, "this week") {
		thisMonday := now.AddDate(0, 0, -(int(now.Weekday())+6)%7)
		return &DateRange{From: thisMonday, To: now, Label: fmt.Sprintf("这周(%s至今)", thisMonday.Format("1月2日"))}, true
	}
	if strings.Contains(question, "今天") || strings.Contains(lower, "today") {
		s := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return &DateRange{From: s, To: s.Add(24*time.Hour - time.Second), Label: s.Format("2006-01-02")}, true
	}
	if strings.Contains(question, "昨天") || strings.Contains(lower, "yesterday") {
		d := now.AddDate(0, 0, -1)
		s := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		return &DateRange{From: s, To: s.Add(24*time.Hour - time.Second), Label: s.Format("2006-01-02")}, true
	}
	if strings.Contains(question, "前天") {
		d := now.AddDate(0, 0, -2)
		s := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		return &DateRange{From: s, To: s.Add(24*time.Hour - time.Second), Label: s.Format("2006-01-02")}, true
	}
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]\s*(?:到|至|~|-|—)\s*(\d{1,2})月(\d{1,2})[号日]`).FindStringSubmatch(question); m != nil {
		m1, _ := strconv.Atoi(m[1])
		d1, _ := strconv.Atoi(m[2])
		m2, _ := strconv.Atoi(m[3])
		d2, _ := strconv.Atoi(m[4])
		from := time.Date(now.Year(), time.Month(m1), d1, 0, 0, 0, 0, loc)
		to := time.Date(now.Year(), time.Month(m2), d2, 23, 59, 59, 0, loc)
		if from.After(now) {
			from = from.AddDate(-1, 0, 0)
			to = to.AddDate(-1, 0, 0)
		}
		return &DateRange{From: from, To: to, Label: fmt.Sprintf("%s到%s", from.Format("1月2日"), to.Format("1月2日"))}, true
	}
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]`).FindStringSubmatch(question); m != nil {
		month, _ := strconv.Atoi(m[1])
		day, _ := strconv.Atoi(m[2])
		t := time.Date(now.Year(), time.Month(month), day, 0, 0, 0, 0, loc)
		if t.After(now.AddDate(0, 0, 1)) {
			t = t.AddDate(-1, 0, 0)
		}
		return &DateRange{From: t, To: t.Add(24*time.Hour - time.Second), Label: t.Format("2006-01-02")}, true
	}
	if m := regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`).FindStringSubmatch(question); m != nil {
		if t, err := time.Parse("2006-01-02", m[0]); err == nil {
			return &DateRange{From: t, To: t.Add(24*time.Hour - time.Second), Label: t.Format("2006-01-02")}, true
		}
	}
	return nil, false
}

// DetectAggregateIntent: "有多少" / "有哪些" / "哪些" / "列出" / "什么公司" / "什么企业".
func DetectAggregateIntent(question string) bool {
	lower := strings.ToLower(question)
	for _, w := range []string{"多少", "几个", "几条", "总数", "how many", "count", "统计", "有哪些", "哪些", "所有", "列出", "什么公司", "什么企业", "哪些公司", "哪些企业", "哪些合同"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// "什么X" pattern where X is an entity type (公司/企业/合同/项目/客户/人)
	if regexp.MustCompile(`什么(公司|企业|合同|项目|客户|人)`).MatchString(question) {
		return true
	}
	return false
}

// DetectRelationIntent: "A和B什么关系" / "A与B如何关联".
func DetectRelationIntent(question string) bool {
	if strings.Contains(question, "什么关系") || strings.Contains(question, "关系是") {
		return true
	}
	if regexp.MustCompile(`.+[和与跟].+`).MatchString(question) &&
		(strings.Contains(question, "关系") || strings.Contains(question, "关联") || strings.Contains(question, "联系")) {
		return true
	}
	return false
}

// LLMExtractKeywords asks the model to pull 2-5 search keywords from a natural-language
// question. Returns space-OR-joined keywords ("keyword1 OR keyword2"). On LLM failure callers
// fall back to SplitCJKKeywords. This is the rich prompt version (proper-noun preservation,
// bilingual mixing) — merged from the dashboard copy.
func LLMExtractKeywords(ctx context.Context, c *llm.Client, question string) (string, error) {
	prompt := fmt.Sprintf(`Extract 2-5 search keywords from this question for a full-text search engine.
Rules:
- Output the KEYWORDS ONLY, space-separated, no explanation
- Keep proper nouns intact (juli, RSA, GLM, makro)
- For Chinese, output individual meaningful words (企业注册, 客户名单, 部署流程), not whole phrases
- Chinese keywords MUST be at least 3 characters; prefer full entity names (瑞福莱暖通, not 暖通)
- Remove question words (什么, 怎么, 吗, 呢, 的, 是, 还记得, 有没有)
- Mix English and Chinese as appropriate to the question

Question: %s

Keywords:`, question)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := c.ChatWithModel(ctx, "glm-4.7-flash", prompt, 100)
	if err != nil {
		return "", err
	}
	resp = strings.TrimSpace(resp)
	if idx := strings.IndexByte(resp, '\n'); idx > 0 {
		resp = resp[:idx]
	}
	resp = strings.Trim(resp, "`\"'.,")
	fields := strings.Fields(resp)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty keywords")
	}
	return strings.Join(fields, " OR "), nil
}

// SplitCJKKeywords extracts searchable keywords from a question by splitting CJK character
// runs into 2-3 char prefixes (the most distinctive part, e.g. 瑞福莱 → 瑞福莱) and keeping
// ASCII words intact. Used as the fallback when LLM keyword extraction is unavailable or empty
// — less precise than the LLM but far better than using the full long sentence as a query
// (which matches nothing). Returns FTS OR-joined keywords.
//
// This is the canonical version: merged from the ask_workflow.go splitCJKKeywords copy (the one
// actually used in production), NOT the dead extractSearchKeywords in handlers.go.
func SplitCJKKeywords(question string) string {
	var tokens []string
	re := regexp.MustCompile(`[A-Za-z0-9.-]{2,}|[\p{Han}]+`)
	for _, m := range re.FindAllString(question, -1) {
		if isCJK(m) {
			// CJK segment: take first 3 chars as the search token (most distinctive part).
			runes := []rune(m)
			if len(runes) >= 2 {
				n := 3
				if n > len(runes) {
					n = len(runes)
				}
				tokens = append(tokens, string(runes[:n]))
			}
		} else if len(m) >= 2 {
			// ASCII word.
			tokens = append(tokens, m)
		}
	}
	// Dedup.
	seen := make(map[string]bool)
	var out []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return question
	}
	return strings.Join(out, " OR ")
}

func isCJK(s string) bool {
	for _, r := range s {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

// ExtractSnippet centers a window on the first keyword found in content, so the LLM sees the
// relevant passage rather than an arbitrary head-truncation. keywords is the OR-joined query
// string OR a pre-split list. Includes CJK-prefix matching: if the full keyword isn't found,
// progressively shorter CJK prefixes are tried. Merged from the dashboard ask_workflow copy.
func ExtractSnippet(content string, keywords []string, windowSize int) string {
	bestPos := -1
	contentLower := strings.ToLower(content)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		kwLower := strings.ToLower(kw)
		if idx := strings.Index(contentLower, kwLower); idx >= 0 {
			bestPos = idx
			break
		}
		// Try shorter CJK prefixes.
		runes := []rune(kw)
		for n := len(runes) - 1; n >= 2; n-- {
			sub := strings.ToLower(string(runes[:n]))
			if idx := strings.Index(contentLower, sub); idx >= 0 {
				bestPos = idx
				break
			}
		}
		if bestPos >= 0 {
			break
		}
	}
	if bestPos < 0 {
		bestPos = 0
	}
	start := bestPos - 100
	if start < 0 {
		start = 0
	}
	end := start + windowSize
	if end > len(content) {
		end = len(content)
	}
	result := content[start:end]
	if start > 0 {
		result = "..." + result
	}
	if end < len(content) {
		result += "..."
	}
	return result
}

// SplitKeywordsForSnippet splits an OR-joined query ("a OR b OR c") into a keyword list for
// ExtractSnippet. Convenience used by callers that built the query via LLMExtractKeywords /
// SplitCJKKeywords (both return OR-joined strings).
func SplitKeywordsForSnippet(searchQuery string) []string {
	return strings.Split(strings.ReplaceAll(strings.ToLower(searchQuery), " or ", "|"), "|")
}
