package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemorySearchTool struct {
	store *store.Store
}

func (t *MemorySearchTool) Name() string { return "memory_search" }

func (t *MemorySearchTool) Description() string {
	return "Search memories by keyword with optional tag, scope, phase, and date filters."
}

func (t *MemorySearchTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"query":    {Type: "string", Description: "Search keyword"},
			"tags":     {Type: "string", Description: "Comma-separated tags (all must match)"},
			"scope":    {Type: "string", Description: "Filter by scope"},
			"phase":    {Type: "string", Description: "Filter by phase: inbox or organized", Enum: []string{"inbox", "organized"}},
			"category": {Type: "string", Description: "Filter by category"},
		},
		Required: []string{"query"},
	}
}

func (t *MemorySearchTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return ErrResult("query is required"), nil
	}

	opts := store.SearchOptions{Query: query}

	if v, ok := params["phase"].(string); ok && v != "" {
		opts.Phase = store.Phase(v)
	}
	if v, ok := params["scope"].(string); ok {
		opts.Scope = v
	}

	results, err := t.store.Search(opts)
	if err != nil {
		return ErrResult(fmt.Sprintf("search failed: %v", err)), err
	}

	data, _ := json.Marshal(results)
	return OkResult(string(data), map[string]any{"count": len(results)}), nil
}
