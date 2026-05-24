package agent

import (
	"context"
	"fmt"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemoryDeleteTool struct {
	store store.Store
}

func (t *MemoryDeleteTool) Name() string { return "memory_delete" }

func (t *MemoryDeleteTool) Description() string {
	return "Delete a memory by ID."
}

func (t *MemoryDeleteTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"id": {Type: "string", Description: "Memory UUID to delete"},
		},
		Required: []string{"id"},
	}
}

func (t *MemoryDeleteTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	id, _ := params["id"].(string)
	if id == "" {
		return ErrResult("id is required"), nil
	}

	if err := t.store.Delete(id); err != nil {
		return ErrResult(fmt.Sprintf("delete failed: %v", err)), err
	}

	return OkResult(fmt.Sprintf("memory %s deleted", id), nil), nil
}
