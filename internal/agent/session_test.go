package agent

import (
	"context"
	"testing"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func setupSession(t *testing.T) (*Session, *store.Store) {
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
	return NewSession(a), s
}

func TestSessionRunJSON(t *testing.T) {
	sess, _ := setupSession(t)

	input := `[{"name":"memory_write","params":{"content":"hello","category":"knowledge"}}]`
	result, err := sess.RunJSON(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("run json: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "memory_write" {
		t.Fatalf("expected memory_write, got %s", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[0].Result.IsError {
		t.Fatalf("unexpected error: %s", result.ToolCalls[0].Result.Content)
	}
}

func TestSessionRunMultiStep(t *testing.T) {
	sess, _ := setupSession(t)

	input := `[
		{"name":"memory_write","params":{"content":"test memory","category":"knowledge","tags":"test"}},
		{"name":"memory_search","params":{"query":"test"}}
	]`
	result, err := sess.RunJSON(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}
}

func TestSessionInjectContext(t *testing.T) {
	sess, s := setupSession(t)

	s.Write("user prefers dark mode", store.PhaseOrganized, store.CategoryPreferences, "global", nil, "test")
	s.Write("user likes vim", store.PhaseOrganized, store.CategoryKnowledge, "global", nil, "test")

	if err := sess.InjectContext("dark mode"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	ctx := sess.Context()
	if ctx == "" {
		t.Fatal("expected non-empty context")
	}
}

func TestSessionRunFailsOnBadTool(t *testing.T) {
	sess, _ := setupSession(t)

	input := `[{"name":"nonexistent","params":{}}]`
	_, err := sess.RunJSON(context.Background(), []byte(input))
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
}

func TestSessionRunInvalidJSON(t *testing.T) {
	sess, _ := setupSession(t)

	_, err := sess.RunJSON(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
}
