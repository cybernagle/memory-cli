package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
)

func tempStore(t *testing.T) (*FileStore, string) {
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

func tempStoreWithTTL(t *testing.T, ttl string) (*FileStore, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Root:         dir,
			ShortTermTTL: ttl,
		},
	}
	s := New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s, dir
}

func mustWrite(t *testing.T, s *FileStore, content string, phase Phase, category Category, scope string, tags []string, source string) *Memory {
	t.Helper()
	mem, err := s.Write(content, phase, category, scope, tags, source)
	if err != nil {
		t.Fatalf("write %q: %v", content, err)
	}
	return mem
}

func TestWriteAndRead(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "hello world", PhaseOrganized, CategoryKnowledge, "global", []string{"test"}, "manual")
	if len(mem.ID) < 32 {
		t.Fatalf("expected full UUID, got short ID: %s", mem.ID)
	}
	if mem.Phase != PhaseOrganized {
		t.Fatalf("expected organized, got %s", mem.Phase)
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

	mem := mustWrite(t, s, "ephemeral", PhaseInbox, CategoryInbox, "global", nil, "manual")
	if mem.ExpiresAt == nil {
		t.Fatal("inbox memory should have expires_at")
	}
	if mem.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires_at should be in the future")
	}
}

func TestWriteLongTermNoExpiry(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "permanent", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	if mem.ExpiresAt != nil {
		t.Fatal("organized memory should not have expires_at")
	}
}

func TestWriteInvalidTTL(t *testing.T) {
	s, _ := tempStoreWithTTL(t, "not-a-duration")

	_, err := s.Write("ephemeral", PhaseInbox, CategoryInbox, "global", nil, "manual")
	if err == nil {
		t.Fatal("expected error for invalid TTL")
	}
}

func TestWriteTTLDays(t *testing.T) {
	s, _ := tempStoreWithTTL(t, "30d")

	mem := mustWrite(t, s, "ephemeral", PhaseInbox, CategoryInbox, "global", nil, "manual")
	if mem.ExpiresAt == nil {
		t.Fatal("inbox memory should have expires_at")
	}
	expectedExpiry := mem.CreatedAt.Add(30 * 24 * time.Hour)
	diff := mem.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("expected expiry near %v, got %v", expectedExpiry, mem.ExpiresAt)
	}
}

func TestDelete(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "to be deleted", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	if err := s.Delete(mem.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.Read(mem.ID)
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

	mustWrite(t, s, "first", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	mustWrite(t, s, "second", PhaseInbox, CategoryInbox, "agent:claude", nil, "manual")
	mustWrite(t, s, "third", PhaseOrganized, CategoryProject, "global", nil, "copilot")

	memories, err := s.List(ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 3 {
		t.Fatalf("expected 3 memories, got %d", len(memories))
	}
}

func TestListFilterByPhase(t *testing.T) {
	s, _ := tempStore(t)

	mustWrite(t, s, "organized one", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	mustWrite(t, s, "inbox one", PhaseInbox, CategoryInbox, "global", nil, "manual")

	memories, err := s.List(ListOptions{Phase: PhaseOrganized})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 organized memory, got %d", len(memories))
	}
	if memories[0].Phase != PhaseOrganized {
		t.Fatalf("expected organized, got %s", memories[0].Phase)
	}
}

func TestListFilterByScope(t *testing.T) {
	s, _ := tempStore(t)

	mustWrite(t, s, "global", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	mustWrite(t, s, "private", PhaseOrganized, CategoryKnowledge, "agent:claude", nil, "manual")

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

	mustWrite(t, s, "from claude", PhaseOrganized, CategoryKnowledge, "global", nil, "claude")
	mustWrite(t, s, "from manual", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

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
		mustWrite(t, s, "memory", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
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

	mem := mustWrite(t, s, "tagged", PhaseOrganized, CategoryKnowledge, "global", []string{"a"}, "manual")

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

	mem := mustWrite(t, s, "test content", PhaseOrganized, CategoryKnowledge, "global", []string{"x"}, "manual")

	path := filepath.Join(dir, "categories", "knowledge", mem.ID+".md")
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

func TestSearchKeyword(t *testing.T) {
	s, _ := tempStore(t)

	mustWrite(t, s, "dark mode preference", PhaseOrganized, CategoryPreferences, "global", nil, "manual")
	mustWrite(t, s, "vim keybindings", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

	results, err := s.Search(SearchOptions{Query: "dark mode"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "dark mode") {
		t.Fatal("result should contain 'dark mode'")
	}
}

func TestSearchByTag(t *testing.T) {
	s, _ := tempStore(t)

	mustWrite(t, s, "pref1", PhaseOrganized, CategoryPreferences, "global", []string{"ui", "preference"}, "manual")
	mustWrite(t, s, "pref2", PhaseOrganized, CategoryKnowledge, "global", []string{"editor"}, "manual")

	results, err := s.Search(SearchOptions{Tags: []string{"ui"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchNoMatch(t *testing.T) {
	s, _ := tempStore(t)

	mustWrite(t, s, "hello", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

	results, err := s.Search(SearchOptions{Query: "nonexistent"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestUpgrade(t *testing.T) {
	s, dir := tempStore(t)

	mem := mustWrite(t, s, "upgrade me", PhaseInbox, CategoryInbox, "global", nil, "manual")
	originalPath := filepath.Join(dir, "categories", "inbox", mem.ID+".md")
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatal("inbox file should exist before upgrade")
	}

	if err := s.Upgrade(mem.ID); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read after upgrade: %v", err)
	}
	if got.Phase != PhaseOrganized {
		t.Fatalf("expected organized phase, got %s", got.Phase)
	}
	if got.ExpiresAt != nil {
		t.Fatal("upgraded memory should not have expires_at")
	}
}

func TestUpgradeOrganizedNoop(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "already organized", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	if err := s.Upgrade(mem.ID); err != nil {
		t.Fatalf("upgrade noop: %v", err)
	}
}

func TestUpgradeNonExistent(t *testing.T) {
	s, _ := tempStore(t)
	if err := s.Upgrade("nonexistent"); err == nil {
		t.Fatal("expected error for non-existent memory")
	}
}

func TestFrontmatterWithDashesInContent(t *testing.T) {
	s, _ := tempStore(t)

	content := "some content\n---\nmore content after dashes"
	mem := mustWrite(t, s, content, PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Content != content {
		t.Fatalf("content mismatch:\nexpected: %q\nactual:   %q", content, got.Content)
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"2h30m", 2*time.Hour + 30*time.Minute},
	}
	for _, tt := range tests {
		got, err := parseDuration(tt.input)
		if err != nil {
			t.Errorf("parseDuration(%q): %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestParseDurationInvalid(t *testing.T) {
	_, err := parseDuration("abc")
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
	_, err = parseDuration("xd")
	if err == nil {
		t.Fatal("expected error for invalid day duration")
	}
}

func TestReadPersistsAccessCount(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "test", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.AccessCount != 1 {
		t.Fatalf("expected access_count=1, got %d", got.AccessCount)
	}

	got2, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read2: %v", err)
	}
	if got2.AccessCount != 2 {
		t.Fatalf("expected access_count=2, got %d", got2.AccessCount)
	}
}

func TestContentHashPersisted(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "hello world", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")
	if mem.ContentHash == "" {
		t.Fatal("expected ContentHash to be set")
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.ContentHash != mem.ContentHash {
		t.Fatalf("ContentHash mismatch: expected %s, got %s", mem.ContentHash, got.ContentHash)
	}
}

func TestFindByHash(t *testing.T) {
	s, _ := tempStore(t)

	mem := mustWrite(t, s, "unique content", PhaseOrganized, CategoryKnowledge, "global", nil, "manual")

	found, err := s.FindByHash(mem.ContentHash)
	if err != nil {
		t.Fatalf("FindByHash: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find memory by hash")
	}
	if found.ID != mem.ID {
		t.Fatalf("wrong memory: expected %s, got %s", mem.ID, found.ID)
	}

	notFound, _ := s.FindByHash("nonexistent-hash")
	if notFound != nil {
		t.Fatal("expected nil for non-existent hash")
	}
}

func fileModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return info.ModTime()
}
