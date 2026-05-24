package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemoryListTool struct {
	store store.Store
}

func (t *MemoryListTool) Name() string { return "memory_list" }

func (t *MemoryListTool) Description() string {
	return "List memories with optional filters for phase, category, scope, source, tags, date range, and limit."
}

func (t *MemoryListTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"phase":          {Type: "string", Description: "Filter by phase: inbox or organized", Enum: []string{"inbox", "organized"}},
			"category":       {Type: "string", Description: "Filter by category: soul,people,project,date,knowledge,..."},
			"scope":          {Type: "string", Description: "Filter by scope"},
			"source":         {Type: "string", Description: "Filter by source"},
			"tags":           {Type: "string", Description: "Comma-separated tags (all must match)"},
			"created_after":  {Type: "string", Description: "ISO 8601 datetime, only memories created after this time"},
			"created_before": {Type: "string", Description: "ISO 8601 datetime, only memories created before this time"},
			"limit":          {Type: "integer", Description: "Max results (default 50)"},
		},
	}
}

func (t *MemoryListTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	opts := store.ListOptions{}

	if v, ok := params["phase"].(string); ok && v != "" {
		opts.Phase = store.Phase(v)
	}
	if v, ok := params["category"].(string); ok && v != "" {
		opts.Category = store.Category(v)
	}
	if v, ok := params["scope"].(string); ok {
		opts.Scope = v
	}
	if v, ok := params["source"].(string); ok {
		opts.Source = v
	}
	if v, ok := params["limit"].(float64); ok {
		opts.Limit = int(v)
	}
	if v, ok := params["tags"].(string); ok && v != "" {
		opts.Tags = strings.Split(v, ",")
	}
	if v, ok := params["created_after"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.CreatedAfter = &t
		}
	}
	if v, ok := params["created_before"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.CreatedBefore = &t
		}
	}

	memories, err := t.store.List(opts)
	if err != nil {
		return ErrResult(fmt.Sprintf("list failed: %v", err)), err
	}

	data, _ := json.Marshal(memories)
	return OkResult(string(data), map[string]any{"count": len(memories)}), nil
}
