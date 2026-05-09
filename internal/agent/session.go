package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
)

type Session struct {
	agent   *Agent
	context string
}

type SessionResult struct {
	ToolCalls []ToolCallResult `json:"tool_calls"`
	Context   string           `json:"context,omitempty"`
}

type ToolCallResult struct {
	Name   string     `json:"name"`
	Params Map        `json:"params"`
	Result ToolResult `json:"result"`
}

type Map = map[string]any

func NewSession(agent *Agent) *Session {
	return &Session{agent: agent}
}

func (s *Session) InjectContext(query string) error {
	results, err := s.agent.store.Search(store.SearchOptions{Query: query})
	if err != nil {
		return fmt.Errorf("inject context: %w", err)
	}

	if len(results) == 0 {
		s.context = ""
		return nil
	}

	var parts []string
	for _, mem := range results {
		parts = append(parts, mem.Content)
	}
	s.context = strings.Join(parts, "\n")
	return nil
}

func (s *Session) Run(ctx context.Context, calls []ToolCall) (*SessionResult, error) {
	s.agent.emit(newEvent(EventAgentStart))

	result := &SessionResult{
		Context: s.context,
	}

	for _, call := range calls {
		toolResult, err := s.agent.Execute(ctx, call.Name, call.Params)
		if err != nil {
			s.agent.emit(newEvent(EventAgentEnd))
			return result, fmt.Errorf("tool %s failed: %w", call.Name, err)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCallResult{
			Name:   call.Name,
			Params: call.Params,
			Result: toolResult,
		})
	}

	s.agent.emit(newEvent(EventAgentEnd))
	return result, nil
}

func (s *Session) RunJSON(ctx context.Context, jsonInput []byte) (*SessionResult, error) {
	var calls []ToolCall
	if err := json.Unmarshal(jsonInput, &calls); err != nil {
		return nil, fmt.Errorf("parse tool calls: %w", err)
	}
	return s.Run(ctx, calls)
}

func (s *Session) Context() string {
	return s.context
}
