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
	"log"
	"os"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/query"
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
			"name":        "memory_read",
			"description": "Read a single memory by ID. Accepts a full UUID or a unique prefix (e.g. the truncated IDs shown by search/list).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "Memory ID or unique prefix"},
				},
				"required": []string{"id"},
			},
		},
		{
			"name":        "memory_delete",
			"description": "Delete a memory by ID. Accepts a full UUID or a unique prefix. Use sparingly — deleted memories cannot be recovered.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "Memory ID or unique prefix"},
				},
				"required": []string{"id"},
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
		{
			"name":        "memory_graduate",
			"description": "Enqueue a business fact to graduate from memory into the project's system of record (PocketBase, CRM...). Use when a work result becomes business data (客户确认了 v26, 合同条款敲定). Without fact, lists the pending queue.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string"},
					"fact":    map[string]any{"type": "string", "description": "The business fact to archive; omit to list the queue"},
				},
				"required": []string{"project"},
			},
		},
		{
			"name":        "memory_state_get",
			"description": "Read a project's shared working state (version, branch/commit, phase, blockers, next actions) written by the previous agent session. Without a project, lists all projects. Stale entries (>24h or written by another session) MUST be verified against git before acting on them.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project name; omit to list all"},
				},
			},
		},
		{
			"name":        "memory_state_set",
			"description": "Report the project's current working state for the next agent session (write at milestones and session end). Precise program-written data — git stays the authority for code; only pointers + semantics git can't express (blockers, next actions, intent).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":  map[string]any{"type": "string"},
					"version":  map[string]any{"type": "string"},
					"branch":   map[string]any{"type": "string"},
					"commit":   map[string]any{"type": "string", "description": "HEAD commit hash"},
					"phase":    map[string]any{"type": "string", "description": "e.g. 开发/测试/部署/验收"},
					"blockers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"next_actions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"notes":    map[string]any{"type": "string", "description": "Free text for the next agent"},
				},
				"required": []string{"project"},
			},
		},
		{
			"name":        "memory_sessions",
			"description": "List per-session work digests: what task each session performed, the entity/facet it revolved around (e.g. 瑞福莱/cases, memory-cli/infra), a summary, and reusable lessons. Use to answer 'what did I/we do before on X'.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Filter by project"},
					"entity":  map[string]any{"type": "string", "description": "Filter by entity, substring match (瑞福莱, marco, juli...)"},
					"session": map[string]any{"type": "string", "description": "One session by id"},
					"limit":   map[string]any{"type": "integer", "description": "Default 10"},
				},
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
	case "memory_read":
		result, err = s.toolRead(args)
	case "memory_delete":
		result, err = s.toolDelete(args)
	case "memory_timeline":
		result, err = s.toolTimeline(args)
	case "memory_list":
		result, err = s.toolList(args)
	case "memory_remind":
		result, err = s.toolRemind(args)
	case "memory_sessions":
		result, err = s.toolSessions(args)
	case "memory_state_get":
		result, err = s.toolStateGet(args)
	case "memory_graduate":
		result, err = s.toolGraduate(args)
	case "memory_state_set":
		result, err = s.toolStateSet(args)
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

func (s *Server) toolRead(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	mem, err := s.store.Read(id)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "ID: %s\n", mem.ID)
	fmt.Fprintf(&sb, "Phase: %s | Category: %s\n", mem.Phase, mem.Category)
	fmt.Fprintf(&sb, "Created: %s\n", mem.CreatedAt.Format("2006-01-02 15:04"))
	if mem.Source != "" {
		fmt.Fprintf(&sb, "Source: %s\n", mem.Source)
	}
	if len(mem.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(mem.Tags, ", "))
	}
	if mem.Project != "" {
		fmt.Fprintf(&sb, "Project: %s\n", mem.Project)
	}
	sb.WriteString("\n" + mem.Content)
	return sb.String(), nil
}

func (s *Server) toolDelete(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	if err := s.store.Delete(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("✓ Memory deleted (id: %s)", id), nil
}

func (s *Server) toolTimeline(args map[string]any) (string, error) {
	// Accept either date ("today"/"昨天"/"2026-08-16") or an explicit from/to range.
	// Range queries span multiple days: summarize from session_views (already digested,
	// one row per session — cheap) instead of raw memories.
	var from, to time.Time
	now := time.Now()
	parseDay := func(s string) (time.Time, bool) {
		if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
			return t, true
		}
		return time.Time{}, false
	}

	fromStr, _ := args["from"].(string)
	toStr, _ := args["to"].(string)
	dateStr, _ := args["date"].(string)

	if fromStr != "" || toStr != "" {
		var ok bool
		if fromStr != "" {
			if from, ok = parseDay(fromStr); !ok {
				return "", fmt.Errorf("invalid from %q (want YYYY-MM-DD)", fromStr)
			}
		} else {
			from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		}
		if toStr != "" {
			if to, ok = parseDay(toStr); !ok {
				return "", fmt.Errorf("invalid to %q (want YYYY-MM-DD)", toStr)
			}
			to = to.Add(24*time.Hour - time.Second)
		} else {
			to = from.Add(24*time.Hour - time.Second)
		}
	} else if dateStr == "today" || dateStr == "今天" || dateStr == "" {
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		to = from.Add(24*time.Hour - time.Second)
	} else if dateStr == "yesterday" || dateStr == "昨天" {
		d := now.AddDate(0, 0, -1)
		from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
		to = from.Add(24*time.Hour - time.Second)
	} else if t, ok := parseDay(dateStr); ok {
		from = t
		to = t.Add(24*time.Hour - time.Second)
	} else {
		d := now.AddDate(0, 0, -1)
		from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
		to = from.Add(24*time.Hour - time.Second)
	}

	days := to.Sub(from).Hours()/24 + 1

	// Multi-day ranges: session digests are the cheap path (pre-aggregated, LLM-free).
	if days > 1.5 {
		views, err := s.store.ListSessionViews(store.SessionViewFilter{Limit: 400})
		if err == nil {
			var in []*store.SessionView
			for _, v := range views {
				t, err := time.Parse(time.RFC3339, v.LastSeen)
				if err == nil && !t.Before(from) && !t.After(to) {
					in = append(in, v)
				}
			}
			if len(in) > 0 {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("%d 个工作会话(%s ~ %s),按时间倒序:\n\n", len(in),
					from.Format("01-02"), to.Format("01-02")))
				for i, v := range in {
					if i >= 40 {
						sb.WriteString(fmt.Sprintf("... 还有 %d 个会话\n", len(in)-40))
						break
					}
					day := ""
					if t, err := time.Parse(time.RFC3339, v.LastSeen); err == nil {
						day = t.Format("01-02")
					}
					line := fmt.Sprintf("[%s] %s", day, v.Task)
					if v.Entity != "" {
						line += "(" + v.Entity
						if v.Facet != "" {
							line += "/" + v.Facet
						}
						line += ")"
					}
					sb.WriteString(line + "\n")
				}
				return sb.String(), nil
			}
		}
	}

	// Single day: raw memories + LLM narrative, sized to stay well under the MCP 30s
	// tool budget (LLM ctx 20s, 60 items, 120 chars each).
	items, err := s.store.List(store.ListOptions{CreatedAfter: &from, CreatedBefore: &to, Limit: 500})
	if err != nil || len(items) == 0 {
		return fmt.Sprintf("No memories for %s", from.Format("2006-01-02")), nil
	}

	if s.llm != nil {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Summarize what the user did on %s based on these records (中文):\n\n", from.Format("2006-01-02")))
		for i, m := range items {
			if i >= 60 {
				break
			}
			c := m.Content
			if len(c) > 120 {
				c = c[:120] + "..."
			}
			sb.WriteString(fmt.Sprintf("[%s] %s\n", m.CreatedAt.Format("15:04"), c))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

// runAskWorkflowMCP handles memory_ask. It delegates the query-understanding steps (time
// intent, keyword extraction, CJK split, snippet) to the shared internal/query package — the
// same one the dashboard uses — so the two entry points can't drift apart. The agent calling
// this tool is itself an LLM that understands context, so unlike the dashboard we skip the
// resolveContext/follow-up-merge stage and go straight to intent detection + search.
func runAskWorkflowMCP(s *Server, question string, history []chatMessage) (string, map[string]any) {
	// Time-scoped question? Summarize the activity timeline for that range.
	if dr, ok := query.DetectTimeIntent(question); ok {
		items, err := s.store.List(store.ListOptions{CreatedAfter: &dr.From, CreatedBefore: &dr.To, Limit: 500})
		if err != nil || len(items) == 0 {
			return fmt.Sprintf("%s 没有记忆记录。", dr.Label), nil
		}
		if s.llm != nil {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("总结用户在 %s 的活动（共%d条）。用中文。\n\n", dr.Label, len(items)))
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
				return ans, map[string]any{"date": dr.Label, "count": len(items)}
			}
		}
		return fmt.Sprintf("%d 条记忆，时间范围 %s", len(items), dr.Label), map[string]any{"count": len(items)}
	}

	// Entity search path: extract keywords + search all phases.
	searchQuery := ""
	if s.llm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		kw, err := query.LLMExtractKeywords(ctx, s.llm, question)
		cancel()
		if err == nil && strings.TrimSpace(kw) != "" {
			searchQuery = kw
		}
	}
	if searchQuery == "" {
		// LLM failed or returned empty — use CJK split fallback.
		searchQuery = query.SplitCJKKeywords(question)
	}

	// Search all phases. Exclude auto-generated aggregates (proposal evidence, profile
	// snapshots) so they don't crowd out real memories in recency ranking — e.g. a "用户是谁"
	// query should surface "User prefers Go/React", not "[topic: writing] accept_rate=0.00".
	excludeAggregates := []string{"evidence-task", "profile-task"}
	inbox, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseInbox, ExcludeSources: excludeAggregates})
	org, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseOrganized, ExcludeSources: excludeAggregates})
	proc, _ := s.store.SearchWithExpansion(store.SearchOptions{Query: searchQuery, Phase: store.PhaseProcessed, ExcludeSources: excludeAggregates})

	combined := append(org, proc...)
	combined = append(combined, inbox...)
	if len(combined) == 0 {
		return "No relevant memories found.", nil
	}
	if len(combined) > 20 {
		combined = combined[:20]
	}

	// Build context for LLM, centering snippets on the matched keywords.
	snippetKWs := query.SplitKeywordsForSnippet(searchQuery)
	var sb strings.Builder

	// Structured grounding first: session digests matching the question's keywords give
	// the LLM pre-aggregated task/entity/lesson context, so answers about "what did I do
	// on X" come out structured instead of being re-synthesized from raw memory fragments.
	if views, err := s.store.SessionViewsMatchingKeywords(snippetKWs, 5); err == nil && len(views) > 0 {
		sb.WriteString("\n=== 相关会话工作摘要(按 session 聚合的派生视图) ===")
		for _, v := range views {
			line := fmt.Sprintf("\n[%s] task: %s", v.LastSeen[:min(10, len(v.LastSeen))], v.Task)
			if v.Entity != "" {
				line += " | entity: " + v.Entity
				if v.Facet != "" {
					line += " / " + v.Facet
				}
			}
			sb.WriteString(line)
			sb.WriteString("\n" + v.Summary)
			var lessons []string
			if json.Unmarshal([]byte(v.Lessons), &lessons) == nil {
				for _, l := range lessons {
					sb.WriteString("\n  ⚑ " + l)
				}
			}
			sb.WriteString("\n")
		}
	}

	for i, m := range combined {
		c := m.Content
		if len(c) > 300 {
			c = query.ExtractSnippet(c, snippetKWs, 300)
		}
		dateStr := m.CreatedAt.Format("2006-01-02")
		if m.Phase == store.PhaseOrganized || m.Phase == store.PhaseProcessed {
			dateStr = "(processed " + dateStr + ")"
		}
		sb.WriteString(fmt.Sprintf("\n[%d] DATE: %s | %s", i+1, dateStr, c))
	}

	prompt := fmt.Sprintf(`Answer based on this context. If not enough info, say so.
Session digests (aggregated per work session, with reusable lessons) come first; raw memory fragments follow. Prefer grounding in the digests for "what did I do / what was learned" questions.

Context:
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

// chatMessage is a single turn of conversation history (passed by the MCP client so follow-up
// questions can be resolved). Kept here because the MCP protocol layer owns it.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
var _ = log.Printf

// toolSessions serves the per-session work digest projection (session_views): task,
// entity/facet, summary, lessons — the "what did I do before on X" read model.
func (s *Server) toolSessions(args map[string]any) (string, error) {
	f := store.SessionViewFilter{Limit: 10}
	if v, ok := args["project"].(string); ok {
		f.Project = v
	}
	if v, ok := args["entity"].(string); ok {
		f.Entity = v
	}
	if v, ok := args["session"].(string); ok {
		f.SessionID = v
	}
	if v, ok := args["limit"].(float64); ok && int(v) > 0 {
		f.Limit = int(v)
	}
	views, err := s.store.ListSessionViews(f)
	if err != nil {
		return "", err
	}
	if len(views) == 0 {
		return "No session digests yet (filters may be too narrow, or run `memory sessions --build N`).", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d sessions:\n\n", len(views)))
	for i, v := range views {
		sb.WriteString(fmt.Sprintf("[%d] %s | project=%s | %d memories\n", i+1, v.LastSeen, v.Project, v.MemoryCount))
		sb.WriteString("    task: " + v.Task + "\n")
		if v.Entity != "" {
			line := "    entity: " + v.Entity
			if v.Facet != "" {
				line += " / " + v.Facet
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("    " + v.Summary + "\n")
		var lessons []string
		if json.Unmarshal([]byte(v.Lessons), &lessons) == nil {
			for _, l := range lessons {
				sb.WriteString("    ⚑ " + l + "\n")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}


// toolStateGet reads the shared project state. No project → compact list of all.
func (s *Server) toolStateGet(args map[string]any) (string, error) {
	project, _ := args["project"].(string)
	if strings.TrimSpace(project) == "" {
		states, err := s.store.ListProjectStates()
		if err != nil {
			return "", err
		}
		if len(states) == 0 {
			return "No project states recorded yet.", nil
		}
		var sb strings.Builder
		if gaps, err := s.store.CoverageGaps(); err == nil && len(gaps) > 0 {
			sb.WriteString("⚠️ Coverage gaps(会话有进展但状态未上报,接手前先核实):\n")
			for _, g := range gaps {
				if g.LastState == "" {
					sb.WriteString(fmt.Sprintf("  - %s:无状态记录;最近工作 %.0fh 前 — %s\n", g.Project, g.DeltaHours, g.TaskHead))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s:状态落后最近会话 %.0fh — %s\n", g.Project, g.DeltaHours, g.TaskHead))
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("%d projects:\n\n", len(states)))
		for i, ps := range states {
			stale := ""
			if ps.Stale {
				stale = " ⚠️STALE(先 git 核实)"
			}
			sb.WriteString(fmt.Sprintf("[%d] %s — %s @ %s(%s) · %s · %.1fh ago by %s%s\n",
				i+1, ps.Project, defaultOf(ps.Version, "-"), defaultOf(ps.Branch, "-"),
				ps.CommitShort(), defaultOf(ps.Phase, "-"), ps.AgeHours, defaultOf(ps.UpdatedBy, "?"), stale))
			if len(ps.NextActions) > 0 {
				sb.WriteString("    next: " + strings.Join(ps.NextActions, " / ") + "\n")
			}
			if len(ps.Blockers) > 0 {
				sb.WriteString("    blockers: " + strings.Join(ps.Blockers, " / ") + "\n")
			}
		}
		return sb.String(), nil
	}
	ps, err := s.store.GetProjectState(project)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(ps)
	staleNote := ""
	if ps.Stale {
		staleNote = "\n⚠️ STALE: 超过 24h 未更新,动工前先用 git 核实 commit/branch。"
	}
	return string(data) + staleNote, nil
}

// toolStateSet reports the project state for the next agent session.
func (s *Server) toolStateSet(args map[string]any) (string, error) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	list := func(k string) []string {
		raw, ok := args[k].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if sv, ok := v.(string); ok {
				out = append(out, sv)
			}
		}
		return out
	}
	ps, err := s.store.SetProjectState(store.StateInput{
		Project: str("project"), Version: str("version"), Branch: str("branch"),
		Commit: str("commit"), Phase: str("phase"),
		Blockers: list("blockers"), NextActions: list("next_actions"),
		Notes: str("notes"),
		UpdatedBy: "mcp/" + defaultOf(str("session"), "anon"), SessionID: str("session"),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✓ state recorded: %s %s @ %s(%s) · %s\nblockers=%d next=%d\n(下一个 session 开局即读;state.md 已刷新)",
		ps.Project, ps.Version, ps.Branch, ps.CommitShort(), ps.Phase, len(ps.Blockers), len(ps.NextActions)), nil
}

func defaultOf(v, def string) string {
	if v == "" {
		return def
	}
	return v
}


// toolGraduate enqueues a business fact for archival into the project's system of
// record, or lists the pending queue when no fact is given.
func (s *Server) toolGraduate(args map[string]any) (string, error) {
	project, _ := args["project"].(string)
	fact, _ := args["fact"].(string)
	if strings.TrimSpace(fact) == "" {
		grads, err := s.store.ListGraduations(true)
		if err != nil {
			return "", err
		}
		if len(grads) == 0 {
			return "Graduation queue empty.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%d pending graduations:\n", len(grads)))
		for _, g := range grads {
			sb.WriteString(fmt.Sprintf("\n[#%d] %s — %s\n     queued %s;archive then `memory graduate done %d --pb <pointer>`",
				g.ID, g.Project, g.Fact, g.CreatedAt[:10], g.ID))
		}
		return sb.String(), nil
	}
	g, err := s.store.AddGraduation(project, fact, "")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✓ queued #%d: %s — %s\n归档到业务系统后执行 memory graduate done %d --pb <指针>",
		g.ID, g.Project, g.Fact, g.ID), nil
}
