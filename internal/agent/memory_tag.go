package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemoryTagTool struct {
	store store.Store
}

func (t *MemoryTagTool) Name() string { return "memory_tag" }

func (t *MemoryTagTool) Description() string {
	return "Add or remove tags on a memory."
}

func (t *MemoryTagTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"id":     {Type: "string", Description: "Memory UUID"},
			"add":    {Type: "string", Description: "Comma-separated tags to add"},
			"remove": {Type: "string", Description: "Comma-separated tags to remove"},
		},
		Required: []string{"id"},
	}
}

func (t *MemoryTagTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	id, _ := params["id"].(string)
	if id == "" {
		return ErrResult("id is required"), nil
	}

	var add, remove []string
	if v, ok := params["add"].(string); ok && v != "" {
		for _, t := range strings.Split(v, ",") {
			if t := strings.TrimSpace(t); t != "" {
				add = append(add, t)
			}
		}
	}
	if v, ok := params["remove"].(string); ok && v != "" {
		for _, t := range strings.Split(v, ",") {
			if t := strings.TrimSpace(t); t != "" {
				remove = append(remove, t)
			}
		}
	}

	mem, err := t.store.Tag(id, add, remove)
	if err != nil {
		return ErrResult(fmt.Sprintf("tag failed: %v", err)), err
	}

	data, _ := json.Marshal(map[string]any{
		"id":   mem.ID,
		"tags": mem.Tags,
	})
	return OkResult(string(data), nil), nil
}
