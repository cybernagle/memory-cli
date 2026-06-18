package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cybernagle/memory-cli/internal/store"
)

type stepType string

const (
	stepThinking  stepType = "thinking"
	stepSearching stepType = "searching"
	stepObserving stepType = "observing"
	stepAnswering stepType = "answering"
)

type agentStep struct {
	Type    stepType `json:"type"`
	Content string   `json:"content"`
	Query   string   `json:"query,omitempty"`
	Results int      `json:"results,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

type agentRequest struct {
	Question         string        `json:"question"`
	MaxSteps         int           `json:"max_steps,omitempty"`
	History          []chatMessage `json:"history,omitempty"`
	MaxContextTokens int           `json:"max_context_tokens,omitempty"`
}

type agentResponse struct {
	Answer   string              `json:"answer"`
	Sources  []map[string]string `json:"sources"`
	Steps    []agentStep         `json:"steps"`
	Question string              `json:"question"`
}

type searchQuery struct {
	Query string   `json:"query"`
	Tags  []string `json:"tags,omitempty"`
	Phase string   `json:"phase,omitempty"`
}

type planResponse struct {
	Queries []searchQuery `json:"queries"`
}

type observeResponse struct {
	Sufficient bool          `json:"sufficient"`
	Thinking   string        `json:"thinking"`
	Queries    []searchQuery `json:"queries,omitempty"`
}

type resultCollector struct {
	seen map[string]bool
	all  []*store.Memory
}

func newResultCollector() *resultCollector {
	return &resultCollector{seen: make(map[string]bool)}
}

func (rc *resultCollector) add(mems []*store.Memory) int {
	added := 0
	for _, m := range mems {
		if !rc.seen[m.ID] {
			rc.seen[m.ID] = true
			rc.all = append(rc.all, m)
			added++
		}
	}
	return added
}

func (srv *Server) handleAskAgent(w http.ResponseWriter, r *http.Request) {
	var req agentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "missing question")
		return
	}
	if req.MaxSteps <= 0 || req.MaxSteps > 10 {
		req.MaxSteps = 5
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sendStep := func(step agentStep) {
		data, _ := json.Marshal(step)
		fmt.Fprintf(w, "event: step\ndata: %s\n\n", data)
		flusher.Flush()
	}

	sendDone := func(resp agentResponse) {
		data, _ := json.Marshal(resp)
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
		flusher.Flush()
	}

	ctx := r.Context()
	var steps []agentStep
	collected := newResultCollector()

	// Trim history to fit maxContextTokens
	history := trimHistory(req.History, req.MaxContextTokens)

	// Run agent loop
	answer, sources := srv.runAgentLoop(ctx, req.Question, req.MaxSteps, sendStep, &steps, collected, history)

	sendDone(agentResponse{
		Answer:   answer,
		Sources:  sources,
		Steps:    steps,
		Question: req.Question,
	})
}

func (srv *Server) runAgentLoop(
	ctx context.Context,
	question string,
	maxSteps int,
	emitStep func(agentStep),
	steps *[]agentStep,
	collected *resultCollector,
	history []chatMessage,
) (string, []map[string]string) {
	llm := srv.llm
	if llm == nil {
		return "LLM not available.", nil
	}

	// Step 1: Plan — decompose question into search queries
	emitStep(agentStep{Type: stepThinking, Content: "Analyzing question..."})
	*steps = append(*steps, agentStep{Type: stepThinking, Content: "Analyzing question..."})

	historySection := buildHistorySection(history)
	planPrompt := fmt.Sprintf(`You are a memory search agent. A user asked: "%s"
%s
Decompose this question into 1-3 SHORT search queries to find relevant memories.
IMPORTANT: Always output ENGLISH keywords only, even if the question is in Chinese/Japanese/Korean.
The search engine uses English FTS5 and cannot match non-English queries.
Each query must be 2-4 KEYWORDS only. No full sentences. No stop words.
Example: for "我有哪些项目？" → ["project list", "active project", "current work"]
Output ONLY valid JSON, no markdown:
{"queries":[{"query":"keyword1 keyword2"}]}`, question, historySection)

	planResp, err := llm.ChatWithTokens(ctx, planPrompt, 1024)
	if err != nil {
		log.Printf("[agent] plan error: %v", err)
		// Fallback: use raw question as single query
		return srv.fallbackSearch(ctx, question, collected, emitStep, steps, history)
	}

	var plan planResponse
	if err := parseAgentJSON(planResp, &plan); err != nil || len(plan.Queries) == 0 {
		log.Printf("[agent] plan parse error: %v, raw: %.200s", err, planResp)
		return srv.fallbackSearch(ctx, question, collected, emitStep, steps, history)
	}

	// Step 2-4: Iterate plan → search → observe
	for i := 0; i < maxSteps; i++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return "Search cancelled.", nil
		default:
		}

		// Execute planned searches
		var totalNew int
		for _, q := range plan.Queries {
			opts := store.SearchOptions{Query: q.Query}

			results, err := srv.store.impl.Search(opts)
			if err != nil {
				log.Printf("[agent] search error: %v", err)
				continue
			}

			added := collected.add(results)
			totalNew += added

			step := agentStep{
				Type:    stepSearching,
				Query:   q.Query,
				Results: len(results),
			}
			emitStep(step)
			*steps = append(*steps, step)
		}

		if collected.all == nil || len(collected.all) == 0 {
			if i >= maxSteps-1 {
				break
			}
			emitStep(agentStep{Type: stepObserving, Content: "No results found, trying different keywords..."})
			*steps = append(*steps, agentStep{Type: stepObserving, Content: "No results found, trying different keywords..."})
			// Ask LLM for different queries
			plan.Queries = []searchQuery{{Query: simplifyQuery(question)}}
			continue
		}

		// Observe: ask LLM if results are sufficient
		observePrompt := buildObservePrompt(question, plan.Queries, collected.all, history)

		obsResp, err := llm.ChatWithTokens(ctx, observePrompt, 1024)
		if err != nil {
			log.Printf("[agent] observe error: %v", err)
			break // Proceed to synthesis with what we have
		}

		var obs observeResponse
		if err := parseAgentJSON(obsResp, &obs); err != nil {
			log.Printf("[agent] observe parse error: %v", err)
			break // Proceed to synthesis
		}

		obsStep := agentStep{Type: stepObserving, Content: obs.Thinking}
		emitStep(obsStep)
		*steps = append(*steps, obsStep)

		if obs.Sufficient || len(obs.Queries) == 0 {
			break
		}

		// Update plan for next iteration
		plan.Queries = obs.Queries
	}

	// Step 5: Synthesize final answer
	emitStep(agentStep{Type: stepAnswering, Content: "Composing answer..."})
	*steps = append(*steps, agentStep{Type: stepAnswering, Content: "Composing answer..."})

	synthPrompt := buildSynthPrompt(question, collected.all, history)

	answer, err := llm.ChatWithTokens(ctx, synthPrompt, 2048)
	if err != nil {
		log.Printf("[agent] synthesize error: %v", err)
		return formatCollected(collected.all), toSources(collected.all)
	}

	return answer, toSources(topN(collected.all, 10))
}

func (srv *Server) fallbackSearch(
	ctx context.Context,
	question string,
	collected *resultCollector,
	emitStep func(agentStep),
	steps *[]agentStep,
	history []chatMessage,
) (string, []map[string]string) {
	emitStep(agentStep{Type: stepSearching, Query: question, Results: 0})
	*steps = append(*steps, agentStep{Type: stepSearching, Query: question, Results: 0})

	results, err := srv.store.impl.Search(store.SearchOptions{Query: question})
	if err == nil {
		collected.add(results)
	}

	if collected.all == nil || len(collected.all) == 0 {
		return "No relevant memories found.", nil
	}

	if srv.llm == nil {
		return formatCollected(collected.all), toSources(collected.all)
	}

	prompt := buildSynthPrompt(question, collected.all, history)
	answer, err := srv.llm.ChatWithTokens(ctx, prompt, 2048)
	if err != nil {
		return formatCollected(collected.all), toSources(collected.all)
	}
	return answer, toSources(topN(collected.all, 10))
}

func buildObservePrompt(question string, queries []searchQuery, results []*store.Memory, history []chatMessage) string {
	var searchInfo strings.Builder
	for _, q := range queries {
		searchInfo.WriteString(fmt.Sprintf("- Query: \"%s\" → %d total results\n", q.Query, len(results)))
	}

	var resultInfo strings.Builder
	limit := 30
	if len(results) < limit {
		limit = len(results)
	}
	for i := 0; i < limit; i++ {
		content := results[i].Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		resultInfo.WriteString(fmt.Sprintf("\n[%d] [%s] %s", i+1, results[i].Category, content))
	}

	return fmt.Sprintf(`You are a memory search agent analyzing search results.

Original question: "%s"

Searches performed:
%s

Search results (showing top %d):
%s

Do you have enough information to answer the question well?
- If YES: respond {"sufficient":true,"thinking":"brief analysis"}
- If NO: respond {"sufficient":false,"thinking":"what's missing","queries":[{"query":"new keywords","tags":[],"phase":""}]}
- Maximum 2 new queries. Try different keywords or broader terms.
- Output ONLY the JSON object.`, question, searchInfo.String(), limit, resultInfo.String())
}

func buildSynthPrompt(question string, results []*store.Memory, history []chatMessage) string {
	var sb strings.Builder
	limit := 30
	if len(results) < limit {
		limit = len(results)
	}
	for i := 0; i < limit; i++ {
		content := results[i].Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n[%d] [%s] %s", i+1, results[i].Category, content))
	}

	historySection := buildHistorySection(history)
	return fmt.Sprintf(`You are a memory assistant. Answer the user's question based ONLY on the following memories. If the memories don't fully answer the question, acknowledge what's missing. Cite memory categories when referencing specific facts.
%s
User's memories:
%s

Question: %s

Answer concisely in the same language as the question:`, historySection, sb.String(), question)
}

func parseAgentJSON(s string, v any) error {
	// Extract JSON from potential markdown wrapping
	jsonStr := s
	// Find first { or [
	start := -1
	for i, c := range jsonStr {
		if c == '{' || c == '[' {
			start = i
			break
		}
	}
	if start >= 0 {
		// Find matching close
		depth := 0
		openChar := jsonStr[start]
		closeChar := byte('}')
		if openChar == '[' {
			closeChar = ']'
		}
		for i := start; i < len(jsonStr); i++ {
			if jsonStr[i] == byte(openChar) {
				depth++
			} else if jsonStr[i] == closeChar {
				depth--
				if depth == 0 {
					jsonStr = jsonStr[start : i+1]
					break
				}
			}
		}
	}
	return json.Unmarshal([]byte(jsonStr), v)
}

func simplifyQuery(query string) string {
	stopWords := map[string]bool{
		"i": true, "me": true, "my": true, "am": true, "is": true, "are": true,
		"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
		"in": true, "on": true, "at": true, "to": true, "for": true, "with": true,
		"what": true, "which": true, "who": true, "how": true, "why": true,
		"do": true, "does": true, "did": true, "have": true, "has": true,
		"working": true, "about": true, "that": true, "this": true, "it": true,
	}

	// Common CJK→English translations for fallback queries
	cjkTranslations := map[string]string{
		"项目": "project", "工作": "work", "语言": "language programming",
		"框架": "framework", "技术": "technology tech", "架构": "architecture",
		"配置": "config", "部署": "deploy", "测试": "test",
		"数据库": "database", "服务器": "server", "代理": "agent proxy",
		"使用": "using use", "开发": "development dev", "设计": "design",
		"问题": "issue problem bug", "功能": "feature", "代码": "code",
		"用户": "user", "系统": "system", "接口": "api interface",
		"性能": "performance", "安全": "security", "模型": "model",
		"工具": "tool", "平台": "platform", "网络": "network",
		"手机": "mobile ios", "应用": "app application",
		"终端": "terminal cli", "界面": "ui interface",
		"哪些": "", "什么": "", "怎么": "", "如何": "",
		"我的": "", "我": "", "的": "", "了": "", "吗": "",
		"是": "", "在": "", "有": "", "和": "", "与": "",
	}

	// Try translating CJK characters to English keywords
	var englishWords []string
	remaining := query
	for len(remaining) > 0 {
		matched := false
		for cjk, eng := range cjkTranslations {
			if strings.HasPrefix(remaining, cjk) {
				if eng != "" {
					englishWords = append(englishWords, eng)
				}
				remaining = remaining[len(cjk):]
				matched = true
				break
			}
		}
		if !matched {
			// Skip non-ASCII chars or keep ASCII
			if remaining[0] < 128 {
				// ASCII char, collect into word
				j := 0
				for j < len(remaining) && remaining[j] < 128 && remaining[j] != ' ' {
					j++
				}
				word := strings.ToLower(remaining[:j])
				if !stopWords[word] && len(word) > 1 {
					englishWords = append(englishWords, word)
				}
				remaining = remaining[j:]
				// Skip spaces
				for len(remaining) > 0 && remaining[0] == ' ' {
					remaining = remaining[1:]
				}
			} else {
				// Skip unknown CJK char
				_, size := utf8.DecodeRuneInString(remaining)
				remaining = remaining[size:]
			}
		}
	}

	if len(englishWords) > 4 {
		englishWords = englishWords[:4]
	}
	if len(englishWords) > 0 {
		return strings.Join(englishWords, " ")
	}
	return query
}

func formatCollected(mems []*store.Memory) string {
	var lines []string
	for _, m := range mems {
		if len(lines) >= 10 {
			break
		}
		content := m.Content
		if len(content) > 150 {
			content = content[:150] + "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", m.Category, content))
	}
	return "Relevant memories:\n" + strings.Join(lines, "\n")
}

func topN(mems []*store.Memory, n int) []*store.Memory {
	if len(mems) <= n {
		return mems
	}
	return mems[:n]
}

// buildHistorySection formats chat history for prompt injection.
func buildHistorySection(history []chatMessage) string {
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nConversation history:")
	for _, m := range history {
		label := "User"
		if m.Role == "assistant" {
			label = "Assistant"
		}
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n%s: %s", label, content))
	}
	sb.WriteString("\n")
	return sb.String()
}

// trimHistory keeps the most recent messages that fit within maxTokens.
// Rough estimate: 1 token ≈ 4 chars for English, ≈ 2 chars for CJK.
func trimHistory(history []chatMessage, maxTokens int) []chatMessage {
	if maxTokens <= 0 {
		maxTokens = 20000
	}
	if len(history) == 0 {
		return nil
	}

	// Estimate char budget (~3 chars/token as rough average)
	charBudget := maxTokens * 3

	// Walk backwards from most recent, accumulating char count
	totalChars := 0
	end := len(history)
	start := end
	for i := end - 1; i >= 0; i-- {
		totalChars += len(history[i].Content)
		if totalChars > charBudget {
			break
		}
		start = i
	}

	return history[start:end]
}
