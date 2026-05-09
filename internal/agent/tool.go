package agent

import "context"

type Tool interface {
	Name() string
	Description() string
	Parameters() ToolSchema
	Execute(ctx context.Context, params map[string]any) (ToolResult, error)
}

type ToolSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]ToolProperty `json:"properties"`
	Required   []string                `json:"required"`
}

type ToolProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type ToolCall struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

type ToolInfo struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Schema      ToolSchema `json:"schema"`
}

func ErrResult(msg string) ToolResult {
	return ToolResult{Content: msg, IsError: true}
}

func OkResult(content string, meta map[string]any) ToolResult {
	return ToolResult{Content: content, Metadata: meta}
}
