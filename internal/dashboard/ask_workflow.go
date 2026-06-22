package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// ─── Ask Workflow Pipeline ───
//
// The chat query flow is structured as a pipeline of stages. Each stage receives the
// askContext (shared mutable state), does its work, and returns whether to continue.
// The intent detected in Stage 2 controls which subsequent stages run.
//
// Pipeline:
//   resolveContext → detectIntent → [intent-specific fetch → rank → snippet → buildPrompt → generate]

// askContext is the shared state flowing through the pipeline.
type askContext struct {
	// ── Input ──
	question string
	history  []chatMessage
	srv      *Server
	r        *http.Request

	// ── Stage outputs (filled progressively) ──
	resolvedQuestion string         // Stage 1: context-resolved question
	intent           string         // Stage 2: detected intent
	results          []*store.Memory // Stage 3-4: search results (ranked, truncated)
	prompt           string         // Stage 6: final LLM prompt
	answer           string         // Stage 7: LLM answer
	extra            map[string]any // metadata for the response (date, count, etc.)

	// ── Intent-specific params (set by Stage 2) ──
	dateRange    *dateRange
	entityKw     string
	relationPair [2]string
}

// Stage is one pipeline step. Returns false to abort the pipeline (early exit).
type Stage func(ctx *askContext) bool

// runAskWorkflow executes the full pipeline and returns the answer + metadata.
func runAskWorkflow(srv *Server, r *http.Request, question string, history []chatMessage) (string, map[string]any) {
	ctx := &askContext{
		question: question,
		history:  history,
		srv:      srv,
		r:        r,
		extra:    make(map[string]any),
	}

	// Stage 1: resolve conversation context (follow-up → standalone question).
	if !stageResolveContext(ctx) {
		return ctx.answer, ctx.extra
	}

	// Stage 2: detect intent.
	if !stageDetectIntent(ctx) {
		return ctx.answer, ctx.extra
	}

	// Stages 3-7 are intent-specific.
	switch ctx.intent {
	case "time":
		runStages(ctx, stageFetchTime, stageBuildTimelinePrompt, stageGenerate)
	case "aggregate":
		runStages(ctx, stageFetchAggregate, stageBuildAggregatePrompt, stageGenerate)
	case "relation":
		runStages(ctx, stageFetchRelation, stageBuildRelationPrompt, stageGenerate)
	default: // "entity" or anything else
		runStages(ctx,
			stageFetchEntity, stageRankEntity, stageSnippet, stageBuildEntityPrompt, stageGenerate)
	}

	return ctx.answer, ctx.extra
}

func runStages(ctx *askContext, stages ...Stage) {
	for _, s := range stages {
		if !s(ctx) {
			return
		}
	}
}

// ─── Stage 1: Resolve Context ───

// stageResolveContext merges follow-up questions with conversation history into a standalone
// question. "具体细节" after "瑞福莱合同" → "瑞福莱合同的细节". If there's no history or the
// question is already standalone, it passes through unchanged.
func stageResolveContext(ctx *askContext) bool {
	ctx.resolvedQuestion = ctx.question

	if len(ctx.history) == 0 {
		return true
	}

	// Find the last user message (the previous topic).
	var lastUserMsg string
	for i := len(ctx.history) - 1; i >= 0; i-- {
		if ctx.history[i].Role == "user" && ctx.history[i].Content != ctx.question {
			lastUserMsg = ctx.history[i].Content
			break
		}
	}
	if lastUserMsg == "" {
		return true
	}

	// Fast path: if the current question is short and looks like a follow-up (no entity names,
	// just modifiers like "具体细节/更多/日期/继续"), prepend the last user message directly.
	// This avoids an extra LLM call and is more reliable than LLM resolution for simple cases.
	if len([]rune(ctx.question)) <= 15 && isLikelyFollowup(ctx.question) {
		ctx.resolvedQuestion = lastUserMsg + " " + ctx.question
		return true
	}

	// Slow path: use LLM to resolve multi-turn context into a standalone question.
	if ctx.srv.llm == nil {
		// No LLM — fall back to simple concatenation.
		ctx.resolvedQuestion = lastUserMsg + " " + ctx.question
		return true
	}

	var sb strings.Builder
	sb.WriteString("以下是对话历史和当前问题。请将当前问题消解为一个完整的独立问题。\n")
	sb.WriteString("规则：\n- 如果当前问题是对上文话题的补充或追问，把上文的关键实体补进去\n")
	sb.WriteString("- 如果当前问题本身是独立的（换了话题），原样返回\n")
	sb.WriteString("- 只输出消解后的问题，不要解释\n\n")
	for _, msg := range ctx.history {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}
		content := msg.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", role, content))
	}
	sb.WriteString(fmt.Sprintf("\n当前问题：%s\n\n消解后的问题：", ctx.question))

	resolvedCtx, cancel := context.WithTimeout(ctx.r.Context(), 15*time.Second)
	defer cancel()
	resolved, err := ctx.srv.llm.ChatWithModel(resolvedCtx, "glm-4.7-flash", sb.String(), 200)
	if err != nil || strings.TrimSpace(resolved) == "" {
		ctx.resolvedQuestion = lastUserMsg + " " + ctx.question
		return true
	}
	resolved = strings.TrimSpace(resolved)
	resolved = strings.Trim(resolved, "\"'`")
	if resolved != "" {
		ctx.resolvedQuestion = resolved
	}
	return true
}

// isLikelyFollowup returns true if the question looks like a modifier on a previous topic
// rather than a standalone question. E.g. "具体细节" / "更多" / "日期" / "继续说说".
func isLikelyFollowup(q string) bool {
	followupWords := []string{"细节", "更多", "日期", "时间", "继续", "具体", "详细", "再说", "补充",
		"然后呢", "还有吗", "之后", "结果", "怎么样", "如何"}
	for _, w := range followupWords {
		if strings.Contains(q, w) {
			return true
		}
	}
	return false
}

// ─── Stage 2: Detect Intent ───

func stageDetectIntent(ctx *askContext) bool {
	q := ctx.resolvedQuestion

	// Time intent.
	if dr, ok := detectTimeIntent(q); ok {
		ctx.intent = "time"
		ctx.dateRange = dr
		return true
	}

	// Aggregate/list intent.
	if detectAggregateIntent(q) {
		ctx.intent = "aggregate"
		// Extract entity keyword for the aggregate fetch.
		for _, kw := range []string{"合同", "企业", "公司", "客户"} {
			if strings.Contains(q, kw) {
				ctx.entityKw = kw
				break
			}
		}
		return true
	}

	// Relation intent.
	if detectRelationIntent(q) {
		if a, b, ok := extractRelationPair(q); ok {
			ctx.intent = "relation"
			ctx.relationPair = [2]string{a, b}
			return true
		}
	}

	// Default: entity search.
	ctx.intent = "entity"
	return true
}

// extractRelationPair pulls two entities from "A和B什么关系" patterns.
func extractRelationPair(question string) (string, string, bool) {
	m := regexp.MustCompile(`(.+?)(?:和|与|跟|and)(.+?)(?:什么|怎么|如何|有).*(?:关系|关联|联系)`).FindStringSubmatch(question)
	if m != nil {
		a := strings.TrimSpace(m[1])
		b := strings.TrimSpace(m[2])
		if a != "" && b != "" {
			return a, b, true
		}
	}
	return "", "", false
}

// ─── Stage 3: Fetch (intent-specific) ───

// stageFetchTime loads memories in the date range.
func stageFetchTime(ctx *askContext) bool {
	dr := ctx.dateRange
	items, err := ctx.srv.store.impl.List(store.ListOptions{
		CreatedAfter:  &dr.from,
		CreatedBefore: &dr.to,
		Limit:         500,
	})
	if err != nil || len(items) == 0 {
		ctx.answer = fmt.Sprintf("%s 没有记忆记录。", dr.label)
		return false
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	ctx.results = items
	ctx.extra["date"] = dr.label
	ctx.extra["count"] = len(items)
	return true
}

// stageFetchAggregate: pure stats via SQL, or entity search + LLM extraction.
func stageFetchAggregate(ctx *askContext) bool {
	question := ctx.resolvedQuestion

	// Check if this is a pure-stats question (no entity enumeration).
	isEntityQuery := regexp.MustCompile(`(什么|哪些|所有|列出)`).MatchString(question) &&
		regexp.MustCompile(`(公司|企业|合同|客户|人|项目)`).MatchString(question)

	if !isEntityQuery {
		// Pure stats: SQL COUNT only.
		sqlStore, ok := ctx.srv.store.impl.(*store.SqliteStore)
		if !ok {
			ctx.answer = "统计查询需要 SQLite 后端。"
			return false
		}
		db := sqlStore.DB()
		var sb strings.Builder
		sb.WriteString("根据记忆数据库的统计：\n\n**记忆总数**\n")
		rows, _ := db.Query("SELECT phase, COUNT(*) FROM memories GROUP BY phase ORDER BY COUNT(*) DESC")
		if rows != nil {
			for rows.Next() {
				var p string
				var c int
				rows.Scan(&p, &c)
				sb.WriteString(fmt.Sprintf("- %s: %d 条\n", p, c))
			}
			rows.Close()
		}
		lower := strings.ToLower(question)
		if strings.Contains(lower, "项目") || strings.Contains(lower, "project") {
			sb.WriteString("\n**项目分布**\n")
			prows, _ := db.Query("SELECT project, COUNT(*) FROM memories WHERE project != '' GROUP BY project ORDER BY COUNT(*) DESC LIMIT 20")
			if prows != nil {
				for prows.Next() {
					var p string
					var c int
					prows.Scan(&p, &c)
					sb.WriteString(fmt.Sprintf("- %s: %d 条\n", p, c))
				}
				prows.Close()
			}
		}
		ctx.answer = sb.String()
		return false // no LLM needed for pure stats
	}

	// Entity enumeration: search for the entity keyword.
	searchKw := ctx.entityKw
	if searchKw == "" {
		searchKw = "公司"
	}
	results, _ := ctx.srv.store.impl.Search(store.SearchOptions{Query: searchKw, Phase: store.PhaseOrganized})
	inboxResults, _ := ctx.srv.store.impl.SearchLike(store.SearchOptions{Query: searchKw, Phase: store.PhaseInbox})
	ctx.results = append(results, inboxResults...)
	if len(ctx.results) > 30 {
		ctx.results = ctx.results[:30]
	}
	ctx.extra["count"] = len(ctx.results)
	if len(ctx.results) == 0 {
		ctx.answer = fmt.Sprintf("没有找到与%s相关的记忆。", searchKw)
		return false
	}
	return true
}

// stageFetchRelation: co-occurrence search for two entities.
func stageFetchRelation(ctx *askContext) bool {
	a, b := ctx.relationPair[0], ctx.relationPair[1]
	allMems, _ := ctx.srv.store.impl.List(store.ListOptions{Limit: 5000})
	var co []*store.Memory
	for _, m := range allMems {
		if strings.Contains(m.Content, a) && strings.Contains(m.Content, b) {
			co = append(co, m)
		}
	}
	if len(co) == 0 {
		ctx.answer = fmt.Sprintf("没有找到 %s 和 %s 同时出现的记忆。", a, b)
		return false
	}
	ctx.results = co
	ctx.extra["count"] = len(co)
	return true
}

// stageFetchEntity: hybrid search (organized FTS + inbox SearchLike).
func stageFetchEntity(ctx *askContext) bool {
	// Extract search keywords via LLM. If LLM fails, split CJK segments into short prefixes.
	searchQuery := ctx.resolvedQuestion
	if ctx.srv.llm != nil {
		kw, err := llmExtractKeywords(ctx.r.Context(), ctx.srv.llm, ctx.resolvedQuestion)
		if err == nil && kw != "" {
			searchQuery = kw
		} else {
			searchQuery = splitCJKKeywords(ctx.resolvedQuestion)
		}
	} else {
		searchQuery = splitCJKKeywords(ctx.resolvedQuestion)
	}
	ctx.extra["searchQuery"] = searchQuery

	// Inbox: IDF-ranked LIKE search.
	inbox, _ := ctx.srv.store.impl.SearchLike(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseInbox,
	})

	// Organized + processed also via SearchLike — FTS can't handle Chinese queries
	// (unicode61 doesn't segment CJK). SearchLike's CJK prefix matching works for all phases.
	orgResults, _ := ctx.srv.store.impl.SearchLike(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseOrganized,
	})
	procResults, _ := ctx.srv.store.impl.SearchLike(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseProcessed,
	})

	// If everything is empty, try the raw question as a last resort.
	if len(inbox) == 0 && len(orgResults) == 0 && len(procResults) == 0 {
		ctx.answer = "No relevant memories found for your question."
		return false
	}

	// Store all three for the rank stage.
	ctx.results = orgResults
	ctx.extra["_procResults"] = procResults
	ctx.extra["_inboxResults"] = inbox
	ctx.extra["_searchQuery"] = searchQuery
	return true
}

// ─── Stage 4: Rank (entity path only) ───

func stageRankEntity(ctx *askContext) bool {
	procResults, _ := ctx.extra["_procResults"].([]*store.Memory)
	inbox, _ := ctx.extra["_inboxResults"].([]*store.Memory)
	orgResults := ctx.results

	// Balanced phase allocation.
	const procLimit = 4
	inboxLimit := 12
	orgLimit := 6
	if len(inbox) < 3 {
		inboxLimit = 6
		orgLimit = 12
	}
	if len(orgResults) > orgLimit {
		orgResults = orgResults[:orgLimit]
	}
	if len(procResults) > procLimit {
		procResults = procResults[:procLimit]
	}
	if len(inbox) > inboxLimit {
		inbox = inbox[:inboxLimit]
	}

	ctx.results = append(orgResults, procResults...)
	ctx.results = append(ctx.results, inbox...)
	return true
}

// ─── Stage 5: Snippet ───

func stageSnippet(ctx *askContext) bool {
	searchQuery, _ := ctx.extra["_searchQuery"].(string)
	if searchQuery == "" {
		searchQuery, _ = ctx.extra["searchQuery"].(string)
	}
	snippetKWs := strings.Split(strings.ReplaceAll(strings.ToLower(searchQuery), " or ", "|"), "|")

	for _, m := range ctx.results {
		if len(m.Content) <= 300 {
			continue
		}
		m.Content = extractSnippet(m.Content, snippetKWs, 300)
	}
	return true
}

// extractSnippet centers a window on the first keyword found in content.
func extractSnippet(content string, keywords []string, windowSize int) string {
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

// ─── Stage 6: Build Prompt (intent-specific) ───

func stageBuildTimelinePrompt(ctx *askContext) bool {
	dr := ctx.dateRange
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("以下是用户在 %s 的活动记录（共%d条）。请总结用户这段时间做了什么、想了什么。用中文回答。\n\n", dr.label, len(ctx.results)))
	for i, m := range ctx.results {
		if i >= 100 {
			sb.WriteString(fmt.Sprintf("... 共 %d 条\n", len(ctx.results)))
			break
		}
		c := m.Content
		if len(c) > 200 {
			c = c[:200] + "..."
		}
		role := m.Role
		if role == "" {
			role = "note"
		}
		sb.WriteString(fmt.Sprintf("[%s][%s] %s\n", m.CreatedAt.Format("01-02 15:04"), role, c))
	}
	ctx.prompt = sb.String()
	return true
}

func stageBuildAggregatePrompt(ctx *askContext) bool {
	question := ctx.resolvedQuestion
	searchKw := ctx.entityKw
	if searchKw == "" {
		searchKw = "相关"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("用户的问题：%s\n\n", question))
	sb.WriteString(fmt.Sprintf("以下是与%s相关的记忆记录（共%d条）。请根据这些记录直接回答用户的问题。\n", searchKw, len(ctx.results)))
	sb.WriteString("要求：只列出与问题直接相关的实体（公司名/合同/客户），不要列出无关内容。用中文回答。\n\n")
	for i, m := range ctx.results {
		c := m.Content
		if len(c) > 300 {
			c = c[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%d] DATE: %s | %s\n", i+1, m.CreatedAt.Format("2006-01-02"), c))
	}
	ctx.prompt = sb.String()
	return true
}

func stageBuildRelationPrompt(ctx *askContext) bool {
	a, b := ctx.relationPair[0], ctx.relationPair[1]
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("以下是同时提到 \"%s\" 和 \"%s\" 的记忆（共%d条）。请总结它们的关系。用中文回答。\n\n", a, b, len(ctx.results)))
	for i, m := range ctx.results {
		if i >= 20 {
			break
		}
		c := m.Content
		if len(c) > 300 {
			c = c[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("[DATE: %s] %s\n", m.CreatedAt.Format("2006-01-02"), c))
	}
	ctx.prompt = sb.String()
	return true
}

func stageBuildEntityPrompt(ctx *askContext) bool {
	// Compute date range from inbox items for the time-span instruction.
	var earliestDate, latestDate string
	for _, m := range ctx.results {
		if m.Phase != store.PhaseInbox {
			continue
		}
		d := m.CreatedAt.Format("2006-01-02")
		if earliestDate == "" || d < earliestDate {
			earliestDate = d
		}
		if latestDate == "" || d > latestDate {
			latestDate = d
		}
	}

	var sb strings.Builder
	for i, m := range ctx.results {
		content := m.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		dateStr := m.CreatedAt.Format("2006-01-02")
		if m.Phase == store.PhaseOrganized || m.Phase == store.PhaseProcessed {
			dateStr = "(processed " + dateStr + ")"
		}
		sb.WriteString(fmt.Sprintf("\n[%d] DATE: %s | %s", i+1, dateStr, content))
		sb.WriteString("\n")
	}

	dateInstruction := ""
	if earliestDate != "" {
		dateInstruction = fmt.Sprintf("\nCRITICAL — DATES: Each memory starts with \"DATE: YYYY-MM-DD\". The real time span is %s to %s. When asked about timing, cite specific dates. Memories marked \"(processed)\" are summary dates, not event dates.", earliestDate, latestDate)
	}

	ctx.prompt = fmt.Sprintf(`You are a memory assistant. Answer the user's question based ONLY on the following memories. If the memories don't fully answer the question, acknowledge what's missing.%s

User's memories:
%s

Question: %s

Answer concisely in the same language as the question:`, dateInstruction, sb.String(), ctx.resolvedQuestion)
	return true
}

// ─── Stage 7: Generate ───

func stageGenerate(ctx *askContext) bool {
	if ctx.srv.llm == nil {
		ctx.answer = "LLM not available."
		return false
	}
	genCtx, cancel := context.WithTimeout(ctx.r.Context(), 60*time.Second)
	defer cancel()

	answer, err := ctx.srv.llm.Chat(genCtx, ctx.prompt)
	if err != nil {
		if count, ok := ctx.extra["count"].(int); ok && count > 0 {
			ctx.answer = fmt.Sprintf("找到 %d 条相关记录，但总结失败：%v", count, err)
		} else {
			ctx.answer = fmt.Sprintf("回答失败：%v", err)
		}
		return false
	}
	ctx.answer = answer
	return true
}

// splitCJKKeywords extracts searchable keywords from a Chinese question by splitting
// CJK character runs into 2-3 char segments (like 瑞福莱 → 瑞福莱) and keeping ASCII
// words intact. This is a fallback when LLM keyword extraction fails — it's less precise
// but far better than using the full long sentence as a LIKE query (which matches nothing).
func splitCJKKeywords(question string) string {
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

// Ensure unused imports are referenced (llm is used by llmExtractKeywords in handlers.go).
var _ = llm.Client{}
