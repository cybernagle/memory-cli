// Package mcp implements a Model Context Protocol (MCP) server for memory-cli.
// It exposes memory operations (ask, search, write, timeline, list, remind) as MCP tools
// that any MCP-compatible client (Claude Code, Cursor, makro) can call via stdio.
//
// The protocol is JSON-RPC 2.0 over stdin/stdout. Three methods:
//   - initialize: handshake (returns server info + capabilities)
//   - tools/list: returns tool definitions (JSON Schema for parameters)
//   - tools/call: executes a tool and returns the result
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// Server is the MCP server. It reads JSON-RPC from stdin, writes to stdout.
type Server struct {
	store   *store.SqliteStore
	llm     *llm.Client
	cfg     *config.Config
}

func NewServer(cfg *config.Config) (*Server, error) {
	s, err := store.NewSqliteStoreFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	var llmClient *llm.Client
	if c, err := llm.NewClient(llm.Config{}); err == nil {
		llmClient = c
	}

	return &Server{store: s, llm: llmClient, cfg: cfg}, nil
}

// Run starts the stdio JSON-RPC loop. Blocks until stdin closes or EOF.
func (s *Server) Run() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			s.writeError(nil, -32700, "Parse error")
			continue
		}

		method, _ := msg["method"].(string)
		id := msg["id"]
		params, _ := msg["params"].(map[string]any)

		switch method {
		case "initialize":
			s.handleInitialize(id)
		case "tools/list":
			s.handleToolsList(id)
		case "tools/call":
			s.handleToolsCall(id, params)
		case "notifications/initialized":
			// Client acknowledges initialization — no response needed (notification has no id).
		default:
			if id != nil {
				s.writeError(id, -32601, "Method not found: "+method)
			}
		}
	}

	s.store.Close()
	return scanner.Err()
}

// ─── JSON-RPC helpers ───

func (s *Server) writeResult(id any, result any) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	s.writeJSON(resp)
}

func (s *Server) writeError(id any, code int, message string) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
	s.writeJSON(resp)
}

func (s *Server) writeJSON(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintln(os.Stdout, string(data))
}

// ─── MCP methods ───

func (s *Server) handleInitialize(id any) {
	s.writeResult(id, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "memory-cli",
			"version": "1.0",
		},
	})
}

func (s *Server) handleToolsList(id any) {
	s.writeResult(id, map[string]any{"tools": toolDefinitions()})
}

// ─── Tool definitions ───

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "memory_ask",
			"description": "Ask a question about your memories. Handles time queries (今天/昨天/上周), entity lookups, follow-ups, and aggregations. Returns a natural-language answer. Use this for conversational queries.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "The question to ask"},
					"history": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object"},
						"description": "Conversation history for follow-up context [{\"role\":\"user\",\"content\":\"...\"}]",
					},
				},
				"required": []string{"question"},
			},
		},
		{
			"name":        "memory_search",
			"description": "Search memories by keyword. Returns raw memory records (not summarized). Use when you need specific records or want to process results yourself.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Search keywords"},
					"phase":   map[string]any{"type": "string", "description": "Filter by phase: inbox/organized/processed"},
					"project": map[string]any{"type": "string", "description": "Filter by project"},
					"limit":   map[string]any{"type": "integer", "description": "Max results (default 10)"},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "memory_write",
			"description": "Write a new memory. For capturing ideas, decisions, facts, or any knowledge to remember.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content":  map[string]any{"type": "string", "description": "The memory content"},
					"category": map[string]any{"type": "string", "description": "Category: knowledge/preferences/decisions/lessons/capture/proposals"},
					"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"project":  map[string]any{"type": "string"},
				},
				"required": []string{"content"},
			},
		},
		{
			"name":        "memory_timeline",
			"description": "Get a narrative summary of activities on a specific day or date range. E.g. 'what did I do today' or 'last week'.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date": map[string]any{"type": "string", "description": "Date: today/yesterday/2026-06-20/6月20号"},
					"from": map[string]any{"type": "string", "description": "Range start (alternative to date)"},
					"to":   map[string]any{"type": "string", "description": "Range end"},
				},
			},
		},
		{
			"name":        "memory_list",
			"description": "List memories by phase, category, or project. For browsing, not searching.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"phase":    map[string]any{"type": "string"},
					"category": map[string]any{"type": "string"},
					"project":  map[string]any{"type": "string"},
					"limit":    map[string]any{"type": "integer", "description": "Default 20"},
				},
			},
		},
		{
			"name":        "memory_remind",
			"description": "Create a time-based reminder. Supports natural language time (下午3点/明天/2小时后).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{"type": "string", "description": "What to remind"},
					"when":    map[string]any{"type": "string", "description": "When: 下午3点/明天/2小时后/2026-06-25"},
				},
				"required": []string{"content", "when"},
			},
		},
	}
}

// ─── Tool execution ───

func (s *Server) handleToolsCall(id any, params map[string]any) {
	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]any)

	var result string
	var err error

	switch name {
	case "memory_ask":
		result, err = s.toolAsk(args)
	case "memory_search":
		result, err = s.toolSearch(args)
	case "memory_write":
		result, err = s.toolWrite(args)
	case "memory_timeline":
		result, err = s.toolTimeline(args)
	case "memory_list":
		result, err = s.toolList(args)
	case "memory_remind":
		result, err = s.toolRemind(args)
	default:
		s.writeError(id, -32601, "Unknown tool: "+name)
		return
	}

	if err != nil {
		s.writeResult(id, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Error: " + err.Error()},
			},
			"isError": true,
		})
		return
	}

	s.writeResult(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": result},
		},
	})
}

// ─── Tool implementations ───

func (s *Server) toolAsk(args map[string]any) (string, error) {
	question, _ := args["question"].(string)
	if question == "" {
		return "", fmt.Errorf("question is required")
	}

	// Parse optional history.
	var history []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if histRaw, ok := args["history"]; ok {
		histJSON, _ := json.Marshal(histRaw)
		json.Unmarshal(histJSON, &history)
	}

	// Convert to chatMessage format that the workflow expects.
	chatHist := make([]chatMessage, len(history))
	for i, h := range history {
		chatHist[i] = chatMessage{Role: h.Role, Content: h.Content}
	}

	// Run the full ask workflow (intent detection → search → LLM generate).
	answer, _ := runAskWorkflowMCP(s, question, chatHist)
	return answer, nil
}

func (s *Server) toolSearch(args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	opts := store.SearchOptions{Query: query}
	if v, ok := args["phase"].(string); ok {
		opts.Phase = store.Phase(v)
	}
	if v, ok := args["project"].(string); ok {
		opts.Project = v
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}

	// Use SearchLike for better Chinese support.
	results, err := s.store.SearchLike(opts)
	if err != nil {
		results, err = s.store.Search(opts)
		if err != nil {
			return "", err
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memories:\n\n", len(results)))
	for i, m := range results {
		date := m.CreatedAt.Format("2006-01-02")
		content := m.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%d] %s | %s | %s\n%s\n\n", i+1, date, m.Phase, m.Category, content))
	}
	return sb.String(), nil
}

func (s *Server) toolWrite(args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	category := store.CategoryInbox
	if v, ok := args["category"].(string); ok && v != "" {
		category = store.Category(v)
	}

	var tags []string
	if tagsRaw, ok := args["tags"].([]any); ok {
		for _, t := range tagsRaw {
			if ts, ok := t.(string); ok {
				tags = append(tags, ts)
			}
		}
	}

	project, _ := args["project"].(string)

	mem := &store.Memory{
		Content:   content,
		Phase:     store.PhaseInbox,
		Category:  category,
		Scope:     "global",
		Tags:      tags,
		Source:    "mcp",
		Project:   project,
		CreatedAt: time.Now(),
	}
	if err := s.store.IngestMemory(mem); err != nil {
		return "", err
	}

	return fmt.Sprintf("✓ Memory written (id: %s, category: %s)", mem.ID[:8], category), nil
}

func (s *Server) toolTimeline(args map[string]any) (string, error) {
	dateStr, _ := args["date"].(string)

	// Parse date using the same logic as detectTimeIntent.
	var from, to time.Time
	now := time.Now()

	if dateStr == "today" || dateStr == "今天" || dateStr == "" {
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	} else if dateStr == "yesterday" || dateStr == "昨天" {
		d := now.AddDate(0, 0, -1)
		from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
	} else if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		from = t
	} else {
		from = now.AddDate(0, 0, -1) // default: yesterday
	}
	to = from.Add(24*time.Hour - time.Second)

	items, err := s.store.List(store.ListOptions{CreatedAfter: &from, CreatedBefore: &to, Limit: 500})
	if err != nil || len(items) == 0 {
		return fmt.Sprintf("No memories for %s", from.Format("2006-01-02")), nil
	}

	if s.llm != nil && len(items) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Summarize what the user did on %s based on these records (中文):\n\n", from.Format("2006-01-02")))
		for i, m := range items {
			if i >= 80 {
				break
			}
			c := m.Content
			if len(c) > 150 {
				c = c[:150] + "..."
			}
			sb.WriteString(fmt.Sprintf("[%s] %s\n", m.CreatedAt.Format("15:04"), c))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		summary, err := s.llm.Chat(ctx, sb.String())
		if err == nil && summary != "" {
			return summary, nil
		}
	}

	// Fallback: just list.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d memories on %s:\n\n", len(items), from.Format("2006-01-02")))
	for i, m := range items {
		if i >= 30 {
			sb.WriteString(fmt.Sprintf("... and %d more\n", len(items)-30))
			break
		}
		c := m.Content
		if len(c) > 100 {
			c = c[:100] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s] %s\n", m.CreatedAt.Format("15:04"), c))
	}
	return sb.String(), nil
}

func (s *Server) toolList(args map[string]any) (string, error) {
	opts := store.ListOptions{}
	if v, ok := args["phase"].(string); ok {
		opts.Phase = store.Phase(v)
	}
	if v, ok := args["category"].(string); ok {
		opts.Category = store.Category(v)
	}
	if v, ok := args["project"].(string); ok {
		opts.Project = v
	}
	opts.Limit = 20
	if v, ok := args["limit"].(float64); ok {
		opts.Limit = int(v)
	}

	memories, err := s.store.List(opts)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d memories:\n\n", len(memories)))
	for i, m := range memories {
		date := m.CreatedAt.Format("2006-01-02 15:04")
		content := m.Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%d] %s | %s | %s\n%s\n\n", i+1, date, m.Phase, m.Category, content))
	}
	return sb.String(), nil
}

func (s *Server) toolRemind(args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	when, _ := args["when"].(string)
	if content == "" || when == "" {
		return "", fmt.Errorf("content and when are required")
	}

	loc := s.cfg.Location()
	triggerAt, _ := daemon.ParseReminderTime(when, loc)

	reminder := &store.Reminder{
		Content:   content,
		TriggerAt: triggerAt,
		Source:    "mcp",
	}
	if err := s.store.CreateReminder(reminder); err != nil {
		return "", err
	}

	return fmt.Sprintf("✓ Reminder set: %s at %s (id: %s)", content, triggerAt.Format("2006-01-02 15:04"), reminder.ID[:8]), nil
}

// runAskWorkflowMCP is a standalone version of the dashboard workflow that works with the
// MCP server's store/llm directly (no HTTP context). It delegates to the same intent
// detection + search logic, but runs with context.Background() instead of *http.Request.
func runAskWorkflowMCP(s *Server, question string, history []chatMessage) (string, map[string]any) {
	// For MCP, we delegate to the search + LLM path directly. The agent (Claude/GPT) calling
	// this tool is itself an LLM that understands context — so we don't need the full
	// resolveContext stage. But we DO want intent detection + proper search.

	// Simple intent check: if it's a time query, do timeline. Otherwise do entity search.
	if dr, ok := detectTimeIntentStandalone(question); ok {
		items, err := s.store.List(store.ListOptions{CreatedAfter: &dr.from, CreatedBefore: &dr.to, Limit: 500})
		if err != nil || len(items) == 0 {
			return fmt.Sprintf("%s 没有记忆记录。", dr.label), nil
		}
		if s.llm != nil {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("总结用户在 %s 的活动（共%d条）。用中文。\n\n", dr.label, len(items)))
			for i, m := range items {
				if i >= 80 {
					break
				}
				c := m.Content
				if len(c) > 150 {
					c = c[:150] + "..."
				}
				sb.WriteString(fmt.Sprintf("[%s] %s\n", m.CreatedAt.Format("01-02 15:04"), c))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ans, err := s.llm.Chat(ctx, sb.String())
			if err == nil && ans != "" {
				return ans, map[string]any{"date": dr.label, "count": len(items)}
			}
		}
		return fmt.Sprintf("%d 条记忆，时间范围 %s", len(items), dr.label), map[string]any{"count": len(items)}
	}

	// Entity search path: extract keywords + search all phases via SearchLike.
	searchQuery := ""
	if s.llm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		kw, err := llmExtractKeywordsStandalone(ctx, s.llm, question)
		cancel()
		if err == nil && strings.TrimSpace(kw) != "" {
			searchQuery = kw
		}
	}
	if searchQuery == "" {
		// LLM failed or returned empty — use CJK split fallback.
		searchQuery = splitCJKKeywordsImpl(question)
	}

	// Search all phases.
	inbox, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseInbox})
	org, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseOrganized})
	proc, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseProcessed})

	combined := append(org, proc...)
	combined = append(combined, inbox...)
	if len(combined) == 0 {
		return "No relevant memories found.", nil
	}
	if len(combined) > 20 {
		combined = combined[:20]
	}

	// Build context for LLM.
	var sb strings.Builder
	for i, m := range combined {
		c := m.Content
		if len(c) > 300 {
			c = extractSnippetStandalone(c, searchQuery, 300)
		}
		dateStr := m.CreatedAt.Format("2006-01-02")
		if m.Phase == store.PhaseOrganized || m.Phase == store.PhaseProcessed {
			dateStr = "(processed " + dateStr + ")"
		}
		sb.WriteString(fmt.Sprintf("\n[%d] DATE: %s | %s", i+1, dateStr, c))
	}

	prompt := fmt.Sprintf(`Answer based on these memories. If not enough info, say so.

Memories:
%s

Question: %s

Answer concisely in the user's language:`, sb.String(), question)

	if s.llm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		answer, err := s.llm.Chat(ctx, prompt)
		if err == nil && answer != "" {
			return answer, map[string]any{"count": len(combined)}
		}
	}

	// Fallback: return raw results.
	result := fmt.Sprintf("Found %d memories:\n", len(combined))
	for i, m := range combined {
		c := m.Content
		if len(c) > 100 {
			c = c[:100] + "..."
		}
		result += fmt.Sprintf("[%d] %s: %s\n", i+1, m.CreatedAt.Format("2006-01-02"), c)
	}
	return result, map[string]any{"count": len(combined)}
}

// ─── Standalone helpers (no dependency on dashboard package) ───

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// detectTimeIntentStandalone checks for time-based questions (simplified standalone version).
func detectTimeIntentStandalone(question string) (*dateRangeStandalone, bool) {
	now := time.Now()
	loc := now.Location()
	lower := strings.ToLower(question)

	if strings.Contains(question, "今天") || strings.Contains(lower, "today") {
		s := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return &dateRangeStandalone{from: s, to: s.Add(24*time.Hour - time.Second), label: s.Format("2006-01-02")}, true
	}
	if strings.Contains(question, "昨天") || strings.Contains(lower, "yesterday") {
		d := now.AddDate(0, 0, -1)
		s := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		return &dateRangeStandalone{from: s, to: s.Add(24*time.Hour - time.Second), label: s.Format("2006-01-02")}, true
	}
	return nil, false
}

type dateRangeStandalone struct {
	from, to time.Time
	label    string
}

func llmExtractKeywordsStandalone(ctx context.Context, c *llm.Client, question string) (string, error) {
	prompt := fmt.Sprintf(`Extract 2-5 search keywords from this question for a full-text search engine.
Rules:
- Output the KEYWORDS ONLY, space-separated, no explanation
- Keep proper nouns intact
- For Chinese, output individual meaningful words, not whole phrases
- Remove question words (什么, 怎么, 吗, 的, 是)

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

func extractSnippetStandalone(content, searchQuery string, windowSize int) string {
	bestPos := -1
	contentLower := strings.ToLower(content)
	for _, kw := range strings.Split(strings.ReplaceAll(strings.ToLower(searchQuery), " or ", "|"), "|") {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if idx := strings.Index(contentLower, strings.ToLower(kw)); idx >= 0 {
			bestPos = idx
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

// splitCJKKeywords extracts searchable keywords from a Chinese question by splitting
// CJK runs into short prefixes and keeping ASCII words intact.
func splitCJKKeywordsImpl(question string) string {
	var tokens []string
	var cjkBuf strings.Builder
	flushCJK := func() {
		seg := cjkBuf.String()
		cjkBuf.Reset()
		if seg == "" {
			return
		}
		runes := []rune(seg)
		n := 3
		if n > len(runes) {
			n = len(runes)
		}
		if n >= 2 {
			tokens = append(tokens, string(runes[:n]))
		}
	}

	for _, r := range question {
		if r >= '\u4e00' && r <= '\u9fff' {
			cjkBuf.WriteRune(r)
		} else {
			flushCJK()
		}
	}
	flushCJK()

	// Also extract ASCII words.
	for _, m := range regexp.MustCompile(`[A-Za-z0-9.-]{2,}`).FindAllString(question, -1) {
		tokens = append(tokens, m)
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

// Suppress unused import warning.
var _ io.Reader = (*os.File)(nil)
var _ = log.Printf
