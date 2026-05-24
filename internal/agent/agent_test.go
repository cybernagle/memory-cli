package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func setupAgent(t *testing.T) (*Agent, store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: dir, ShortTermTTL: "24h"},
	}
	s := store.New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	a := New(s)
	RegisterAll(a, s)
	return a, s
}

func TestAgentExecuteWrite(t *testing.T) {
	a, _ := setupAgent(t)

	result, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content":  "user prefers dark mode",
		"category": "knowledge",
		"tags":     "preference,ui",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Metadata["id"] == nil {
		t.Fatal("expected id in metadata")
	}
}

func TestAgentExecuteWriteAndRead(t *testing.T) {
	a, _ := setupAgent(t)

	writeResult, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content":  "hello world",
		"category": "knowledge",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	id, _ := writeResult.Metadata["id"].(string)
	readResult, err := a.Execute(context.Background(), "memory_read", map[string]any{
		"id": id,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readResult.IsError {
		t.Fatalf("read error: %s", readResult.Content)
	}
}

func TestAgentExecuteToolNotFound(t *testing.T) {
	a, _ := setupAgent(t)

	_, err := a.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestAgentBeforeToolCallHook(t *testing.T) {
	a, _ := setupAgent(t)

	var called atomic.Int32
	a.hooks.BeforeToolCall = func(ctx context.Context, name string, params map[string]any) (bool, error) {
		called.Add(1)
		return false, nil
	}

	result, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content": "blocked",
	})
	if err == nil {
		t.Fatal("expected error when tool is blocked")
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if called.Load() != 1 {
		t.Fatal("hook not called")
	}
}

func TestAgentAfterToolCallHook(t *testing.T) {
	a, _ := setupAgent(t)

	var called atomic.Int32
	a.hooks.AfterToolCall = func(ctx context.Context, name string, result ToolResult) (ToolResult, error) {
		called.Add(1)
		result.Content = "modified"
		return result, nil
	}

	result, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content": "test",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Content != "modified" {
		t.Fatalf("expected modified content, got: %s", result.Content)
	}
	if called.Load() != 1 {
		t.Fatal("after hook not called")
	}
}

func TestAgentSubscribe(t *testing.T) {
	a, _ := setupAgent(t)

	ch := a.Subscribe()

	_, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content": "test events",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	events := []Event{}
	for {
		select {
		case e := <-ch:
			events = append(events, e)
			if e.Type == EventToolEnd || e.Type == EventError {
				goto done
			}
		default:
			goto done
		}
	}
done:

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Type != EventToolStart {
		t.Fatalf("expected tool_start, got %s", events[0].Type)
	}
}

func TestAgentListTools(t *testing.T) {
	a, _ := setupAgent(t)

	infos := a.ListTools()
	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
	}

	for _, name := range []string{"memory_write", "memory_read", "memory_delete", "memory_list", "memory_search", "memory_tag"} {
		if !names[name] {
			t.Fatalf("missing tool: %s", name)
		}
	}
}

func TestAgentExecuteAll(t *testing.T) {
	a, _ := setupAgent(t)

	results, err := a.ExecuteAll(context.Background(), []ToolCall{
		{Name: "memory_write", Params: map[string]any{"content": "first", "category": "knowledge"}},
		{Name: "memory_write", Params: map[string]any{"content": "second", "category": "knowledge"}},
	})
	if err != nil {
		t.Fatalf("execute all: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestAgentOnEventHook(t *testing.T) {
	a, _ := setupAgent(t)

	var count atomic.Int32
	a.hooks.OnEvent = func(event Event) {
		count.Add(1)
	}

	_, err := a.Execute(context.Background(), "memory_write", map[string]any{
		"content": "test",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if count.Load() == 0 {
		t.Fatal("onEvent hook never called")
	}
}
