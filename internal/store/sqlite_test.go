package store

import (
	"path/filepath"
	"testing"
	"time"
)

func tempSqliteStore(t *testing.T) *SqliteStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSqliteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSqliteWriteAndRead(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.Write("hello world", PhaseOrganized, CategoryKnowledge, "global", []string{"test"}, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if mem.ID == "" {
		t.Fatal("expected ID")
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Content != "hello world" {
		t.Errorf("content = %q, want %q", got.Content, "hello world")
	}
	if got.AccessCount != 1 {
		t.Errorf("access_count = %d, want 1", got.AccessCount)
	}
}

func TestSqliteWriteToInbox(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.WriteToInbox("inbox item", "global", []string{"tag1"}, "test")
	if err != nil {
		t.Fatalf("write to inbox: %v", err)
	}
	if mem.Phase != PhaseInbox {
		t.Errorf("phase = %q, want inbox", mem.Phase)
	}
	if mem.ExpiresAt == nil {
		t.Fatal("expected expires_at for inbox memory")
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "tag1" {
		t.Errorf("tags = %v, want [tag1]", mem.Tags)
	}
}

func TestSqliteDelete(t *testing.T) {
	s := tempSqliteStore(t)

	mem, _ := s.Write("to delete", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	if err := s.Delete(mem.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.FindByID(mem.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestSqliteList(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("mem1", PhaseOrganized, CategoryKnowledge, "global", []string{"go"}, "test")
	s.Write("mem2", PhaseInbox, CategoryInbox, "global", []string{"go", "team"}, "test")
	s.Write("mem3", PhaseOrganized, CategoryPeople, "agent:claude", []string{"go"}, "test")

	all, err := s.List(ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("total = %d, want 3", len(all))
	}

	inbox, _ := s.List(ListOptions{Phase: PhaseInbox})
	if len(inbox) != 1 {
		t.Errorf("inbox = %d, want 1", len(inbox))
	}

	knowledge, _ := s.List(ListOptions{Category: CategoryKnowledge})
	if len(knowledge) != 1 {
		t.Errorf("knowledge = %d, want 1", len(knowledge))
	}

	scopeFiltered, _ := s.List(ListOptions{Scope: "agent:claude"})
	if len(scopeFiltered) != 1 {
		t.Errorf("scope filtered = %d, want 1", len(scopeFiltered))
	}

	tagFiltered, _ := s.List(ListOptions{Tags: []string{"go"}})
	if len(tagFiltered) != 3 {
		t.Errorf("tag filtered = %d, want 3", len(tagFiltered))
	}
}

func TestSqliteSearch(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("dark mode preference", PhaseOrganized, CategoryPreferences, "global", nil, "test")
	s.Write("light mode default", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("unrelated content", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	results, err := s.Search(SearchOptions{Query: "dark"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("results = %d, want 1", len(results))
	}
	if results[0].Content != "dark mode preference" {
		t.Errorf("content = %q", results[0].Content)
	}
}

func TestSqliteTag(t *testing.T) {
	s := tempSqliteStore(t)

	mem, _ := s.Write("tagged", PhaseOrganized, CategoryKnowledge, "global", []string{"a"}, "test")
	updated, err := s.Tag(mem.ID, []string{"b", "c"}, []string{"a"})
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	got := make(map[string]bool)
	for _, t := range updated.Tags {
		got[t] = true
	}
	if !got["b"] || !got["c"] || got["a"] {
		t.Errorf("tags = %v, want [b c]", updated.Tags)
	}
}

func TestSqliteUpgrade(t *testing.T) {
	s := tempSqliteStore(t)

	mem, _ := s.WriteToInbox("upgrade me", "global", nil, "test")
	if mem.Phase != PhaseInbox {
		t.Fatal("expected inbox phase")
	}

	if err := s.Upgrade(mem.ID); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got, _ := s.FindByID(mem.ID)
	if got.Phase != PhaseOrganized {
		t.Errorf("phase = %q, want organized", got.Phase)
	}
	if got.ExpiresAt != nil {
		t.Error("expected nil expires_at after upgrade")
	}
}

func TestSqliteFindByHash(t *testing.T) {
	s := tempSqliteStore(t)

	mem, _ := s.Write("unique content", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	found, err := s.FindByHash(mem.ContentHash)
	if err != nil {
		t.Fatalf("find by hash: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find memory by hash")
	}
	if found.ID != mem.ID {
		t.Errorf("id = %q, want %q", found.ID, mem.ID)
	}
}

func TestSqliteLinks(t *testing.T) {
	s := tempSqliteStore(t)

	mem1, _ := s.Write("memory one", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	mem2, _ := s.Write("memory two", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	if err := s.LinkMemories(mem1.ID, mem2.ID); err != nil {
		t.Fatalf("link: %v", err)
	}

	backlinks, err := s.GetBacklinks(mem1.ID)
	if err != nil {
		t.Fatalf("backlinks: %v", err)
	}
	if len(backlinks) != 1 {
		t.Errorf("backlinks = %d, want 1", len(backlinks))
	}

	if err := s.UnlinkMemories(mem1.ID, mem2.ID); err != nil {
		t.Fatalf("unlink: %v", err)
	}

	backlinks2, _ := s.GetBacklinks(mem1.ID)
	if len(backlinks2) != 0 {
		t.Errorf("backlinks after unlink = %d, want 0", len(backlinks2))
	}
}

func TestSqliteExpiry(t *testing.T) {
	s := tempSqliteStore(t)

	mem, _ := s.WriteToInbox("expiring soon", "global", nil, "test")
	mem.ExpiresAt = timePtr(time.Now().Add(-1 * time.Hour))
	s.db.Exec("UPDATE memories SET expires_at = ? WHERE id = ?",
		mem.ExpiresAt.Format(time.RFC3339), mem.ID)

	all, _ := s.List(ListOptions{Phase: PhaseInbox})
	expired := 0
	for _, m := range all {
		if m.ExpiresAt != nil && m.ExpiresAt.Before(time.Now()) {
			s.Delete(m.ID)
			expired++
		}
	}
	if expired != 1 {
		t.Errorf("expired = %d, want 1", expired)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
