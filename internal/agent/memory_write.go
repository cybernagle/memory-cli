package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cybernagle/memory-cli/internal/ingest"
	"github.com/cybernagle/memory-cli/internal/store"
)

type MemoryWriteTool struct {
	store store.Store
}

func (t *MemoryWriteTool) Name() string { return "memory_write" }

func (t *MemoryWriteTool) Description() string {
	return "Write a new memory. Always writes to inbox. LLM pipeline will extract and organize long-term memories."
}

func (t *MemoryWriteTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"content":  {Type: "string", Description: "Memory content to store"},
			"category": {Type: "string", Description: "Category: soul,people,project,date,knowledge,feedback,preferences,decisions,lessons,habits,skills"},
			"scope":    {Type: "string", Description: "Scope: global, agent:claude, etc."},
			"tags":     {Type: "string", Description: "Comma-separated tags"},
			"source":   {Type: "string", Description: "Source identifier"},
		},
		Required: []string{"content"},
	}
}

func (t *MemoryWriteTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	content, _ := params["content"].(string)
	if content == "" {
		return ErrResult("content is required"), nil
	}

	scope, _ := params["scope"].(string)
	source, _ := params["source"].(string)

	var tags []string
	if raw, ok := params["tags"].(string); ok {
		tags = parseTags(raw)
	}

	var mem *store.Memory
	var err error

	mem, err = t.store.WriteToInbox(content, scope, tags, source, ingest.CurrentProject(), ingest.CurrentTmuxSession())
	if err != nil {
		return ErrResult(fmt.Sprintf("write failed: %v", err)), err
	}

	data, _ := json.Marshal(map[string]string{
		"id":      mem.ID,
		"phase":   string(mem.Phase),
		"scope":   mem.Scope,
		"expires": formatTime(mem.ExpiresAt),
	})
	return OkResult(string(data), map[string]any{"id": mem.ID}), nil
}
