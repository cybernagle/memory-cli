package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/cybernagle/memory-cli/internal/store"
)

type Agent struct {
	store       store.Store
	tools       map[string]Tool
	hooks       Hooks
	subscribers []chan Event
	mu          sync.RWMutex
}

type Option func(*Agent)

func WithHooks(h Hooks) Option {
	return func(a *Agent) { a.hooks = h }
}

func New(s store.Store, opts ...Option) *Agent {
	a := &Agent{
		store: s,
		tools: make(map[string]Tool),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *Agent) RegisterTool(tool Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools[tool.Name()] = tool
}

func (a *Agent) Execute(ctx context.Context, toolName string, params map[string]any) (ToolResult, error) {
	a.mu.RLock()
	tool, ok := a.tools[toolName]
	a.mu.RUnlock()

	if !ok {
		err := fmt.Errorf("tool not found: %s", toolName)
		a.emit(errorEvent(toolName, err.Error()))
		return ErrResult(err.Error()), err
	}

	a.emit(toolEvent(EventToolStart, toolName, params))

	if a.hooks.BeforeToolCall != nil {
		allowed, err := a.hooks.BeforeToolCall(ctx, toolName, params)
		if err != nil {
			a.emit(errorEvent(toolName, err.Error()))
			return ErrResult(err.Error()), err
		}
		if !allowed {
			msg := fmt.Sprintf("tool %s blocked by before_tool_call hook", toolName)
			a.emit(errorEvent(toolName, msg))
			return ErrResult(msg), fmt.Errorf("%s", msg)
		}
	}

	result, err := tool.Execute(ctx, params)
	if err != nil {
		a.emit(errorEvent(toolName, err.Error()))
		return ErrResult(err.Error()), err
	}

	if a.hooks.AfterToolCall != nil {
		modified, err := a.hooks.AfterToolCall(ctx, toolName, result)
		if err != nil {
			a.emit(errorEvent(toolName, err.Error()))
			return ErrResult(err.Error()), err
		}
		result = modified
	}

	a.emit(toolEvent(EventToolEnd, toolName, result))
	return result, nil
}

func (a *Agent) ExecuteAll(ctx context.Context, calls []ToolCall) ([]ToolResult, error) {
	results := make([]ToolResult, 0, len(calls))
	for _, call := range calls {
		result, err := a.Execute(ctx, call.Name, call.Params)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (a *Agent) Subscribe() <-chan Event {
	a.mu.Lock()
	defer a.mu.Unlock()

	ch := make(chan Event, 64)
	a.subscribers = append(a.subscribers, ch)
	return ch
}

func (a *Agent) ListTools() []ToolInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	infos := make([]ToolInfo, 0, len(a.tools))
	for _, tool := range a.tools {
		infos = append(infos, ToolInfo{
			Name:        tool.Name(),
			Description: tool.Description(),
			Schema:      tool.Parameters(),
		})
	}
	return infos
}

func (a *Agent) emit(event Event) {
	if a.hooks.OnEvent != nil {
		a.hooks.OnEvent(event)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, ch := range a.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}
