package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemoryReadTool struct {
	store store.Store
}

func (t *MemoryReadTool) Name() string { return "memory_read" }

func (t *MemoryReadTool) Description() string {
	return "Read a memory by ID. Increments access count."
}

func (t *MemoryReadTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"id": {Type: "string", Description: "Memory UUID"},
		},
		Required: []string{"id"},
	}
}

func (t *MemoryReadTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	id, _ := params["id"].(string)
	if id == "" {
		return ErrResult("id is required"), nil
	}

	mem, err := t.store.Read(id)
	if err != nil {
		return ErrResult(fmt.Sprintf("read failed: %v", err)), err
	}

	data, _ := json.Marshal(mem)
	return OkResult(string(data), nil), nil
}
