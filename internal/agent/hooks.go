package agent

import "context"

type Hooks struct {
	BeforeToolCall func(ctx context.Context, name string, params map[string]any) (bool, error)
	AfterToolCall  func(ctx context.Context, name string, result ToolResult) (ToolResult, error)
	OnEvent        func(event Event)
}
