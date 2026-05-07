package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
)

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Root:         dir,
			ShortTermTTL: "24h",
		},
	}
	s := New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s, dir
}

func TestWriteAndRead(t *testing.T) {
	s, _ := tempStore(t)

	mem, err := s.Write("hello world", LongTerm, "global", []string{"test"}, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if mem.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if mem.Type != LongTerm {
		t.Fatalf("expected long, got %s", mem.Type)
	}
	if mem.Content != "hello world" {
		t.Fatalf("expected 'hello world', got '%s'", mem.Content)
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Content != "hello world" {
		t.Fatalf("content mismatch: %s", got.Content)
	}
	if got.AccessCount != 1 {
		t.Fatalf("expected access_count=1, got %d", got.AccessCount)
	}
}

func TestWriteShortTermHasExpiry(t *testing.T) {
	s, _ := tempStore(t)

	mem, err := s.Write("ephemeral", ShortTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if mem.ExpiresAt == nil {
		t.Fatal("short-term memory should have expires_at")
	}
	if mem.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires_at should be in the future")
	}
}

func TestWriteLongTermNoExpiry(t *testing.T) {
	s, _ := tempStore(t)

	mem, err := s.Write("permanent", LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if mem.ExpiresAt != nil {
		t.Fatal("long-term memory should not have expires_at")
	}
}

func TestDelete(t *testing.T) {
	s, _ := tempStore(t)

	mem, err := s.Write("to be deleted", LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Delete(mem.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.Read(mem.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDeleteNonExistent(t *testing.T) {
	s, _ := tempStore(t)

	err := s.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent memory")
	}
}

func TestReadNonExistent(t *testing.T) {
	s, _ := tempStore(t)

	_, err := s.Read("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent memory")
	}
}

func TestListAll(t *testing.T) {
	s, _ := tempStore(t)

	s.Write("first", LongTerm, "global", nil, "manual")
	s.Write("second", ShortTerm, "agent:claude", nil, "manual")
	s.Write("third", LongTerm, "global", nil, "copilot")

	memories, err := s.List(ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 3 {
		t.Fatalf("expected 3 memories, got %d", len(memories))
	}
}

func TestListFilterByType(t *testing.T) {
	s, _ := tempStore(t)

	s.Write("long one", LongTerm, "global", nil, "manual")
	s.Write("short one", ShortTerm, "global", nil, "manual")

	memories, err := s.List(ListOptions{Type: LongTerm})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 long-term memory, got %d", len(memories))
	}
	if memories[0].Type != LongTerm {
		t.Fatalf("expected long-term, got %s", memories[0].Type)
	}
}

func TestListFilterByScope(t *testing.T) {
	s, _ := tempStore(t)

	s.Write("global", LongTerm, "global", nil, "manual")
	s.Write("private", LongTerm, "agent:claude", nil, "manual")

	memories, err := s.List(ListOptions{Scope: "agent:claude"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 scoped memory, got %d", len(memories))
	}
}

func TestListFilterBySource(t *testing.T) {
	s, _ := tempStore(t)

	s.Write("from claude", LongTerm, "global", nil, "claude")
	s.Write("from manual", LongTerm, "global", nil, "manual")

	memories, err := s.List(ListOptions{Source: "claude"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 source-filtered memory, got %d", len(memories))
	}
}

func TestListLimit(t *testing.T) {
	s, _ := tempStore(t)

	for i := 0; i < 5; i++ {
		s.Write("memory", LongTerm, "global", nil, "manual")
	}

	memories, err := s.List(ListOptions{Limit: 3})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 3 {
		t.Fatalf("expected 3 limited memories, got %d", len(memories))
	}
}

func TestTag(t *testing.T) {
	s, _ := tempStore(t)

	mem, _ := s.Write("tagged", LongTerm, "global", []string{"a"}, "manual")

	updated, err := s.Tag(mem.ID, []string{"b", "c"}, []string{"a"})
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	tagSet := make(map[string]bool)
	for _, tag := range updated.Tags {
		tagSet[tag] = true
	}
	if tagSet["a"] {
		t.Fatal("tag 'a' should have been removed")
	}
	if !tagSet["b"] || !tagSet["c"] {
		t.Fatal("tags 'b' and 'c' should have been added")
	}
}

func TestMarkdownFileFormat(t *testing.T) {
	s, dir := tempStore(t)

	mem, _ := s.Write("test content", LongTerm, "global", []string{"x"}, "manual")

	path := filepath.Join(dir, "long-term", mem.ID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := string(data)
	if content[0:4] != "---\n" {
		t.Fatal("file should start with frontmatter")
	}
	if len(content) < 8 {
		t.Fatal("file too short")
	}
}

func TestListEmpty(t *testing.T) {
	s, _ := tempStore(t)

	memories, err := s.List(ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("expected 0 memories, got %d", len(memories))
	}
}
