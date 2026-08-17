package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

	mem, err := s.WriteToInbox("inbox item", CategoryInbox, "global", []string{"tag1"}, "test", "", "")
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
	// mem2 content is signal-less (no ASCII keyword) so auto-categorization leaves it inbox.
	s.Write("备忘", PhaseInbox, CategoryInbox, "global", []string{"go", "team"}, "test")
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

// TestSqliteSearchLikeASCIIFast is a regression test for an N+1 full-table-scan that made
// any common ASCII keyword (e.g. "user", "React") hang the MCP server for ~20+ minutes.
//
// Root cause: the per-memory scoring loop ran a `SELECT COUNT(*) ... LIKE '%prefix%'`
// subquery for EVERY memory that didn't contain the full keyword — and for ASCII keywords
// the "prefix" was a meaningless substring ("us", "use") that matched thousands of docs.
// With 13k memories that's ~10k full-table scans × 143ms ≈ 24 minutes.
//
// The fix precomputes prefix IDF once per keyword and skips prefix expansion for ASCII
// keywords entirely (only CJK entity names benefit from it). This test seeds enough memories
// that a naive per-memory subquery would be visibly slow, then asserts the search completes
// well under the budget a healthy run needs. A regressed build would take minutes.
// TestSqliteSearchLikeMultiWord is a regression test for ISSUE-001: multi-word keyword queries
// ("橘粒科技 合同 报价") returned 0 results because SearchLike only split on "|"/" OR ", not on
// whitespace — so the whole space-separated string became one keyword that matched nothing.
//
// The fix splits on whitespace (and commas) too. CJK entity names ("瑞福莱暖通设备") contain no
// spaces, so they remain indivisible keywords; only genuine multi-word queries fan out.
func TestSqliteSearchLikeMultiWord(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("橘粒科技和瑞福莱暖通签订网站开发合同，报价5万4", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("项目技术架构采用 React + Next.js", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("unrelated note about weather", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	// Space-separated multi-word query — must fan out into 3 keywords and find the contract.
	results, err := s.SearchLike(SearchOptions{Query: "橘粒科技 合同 报价"})
	if err != nil {
		t.Fatalf("SearchLike: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("ISSUE-001 regression: multi-word query returned 0 results")
	}
	// The contract memory should be top — it matches the rare keywords 橘粒/合同/报价.
	if !strings.Contains(results[0].Content, "橘粒") {
		t.Errorf("top result for multi-word query should be the contract memory, got: %q", results[0].Content)
	}

	// Also confirm it flows through Search() (which routes multi-word to SearchLike).
	searchResults, err := s.Search(SearchOptions{Query: "橘粒科技 合同"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(searchResults) == 0 {
		t.Fatal("Search() routed multi-word query returned 0 — multi-word bypass to SearchLike broken")
	}
}

// TestResolveIDPrefix verifies that Read/Delete accept a unique UUID prefix (the truncated IDs
// shown by search/list), not just the full UUID. This fixes the UX gap where list shows
// "58e541e0" but `read 58e541e0` returned not-found.
// TestWritePathsTriggerSupersede is the chaos-point-② regression test: supersede must fire on
// EVERY write path that produces an organized/processed memory, not just the one manual caller
// that used to remember. We write an old fact, then write a newer overlapping version via each
// entry point and assert the old one got marked superseded.
//
// Before the fix, only cmd/process.go called CheckAndSupersede manually — daemon consolidate,
// processor, direct Write/IngestMemory all bypassed it. Now IngestMemory (the unified chokepoint)
// triggers it post-insert, and Write/WriteToInbox/processor all route through IngestMemory.
func TestWritePathsTriggerSupersede(t *testing.T) {
	mkVersion := func(t *testing.T, s *SqliteStore, amount string) *Memory {
		t.Helper()
		// Same entity (瑞福莱) + same predicate (合同金额) + enough shared CJK terms to cross
		// the ≥3-shared-terms supersede threshold.
		mem, err := s.Write(
			"瑞福莱暖通设备合同金额"+amount+"元，甲方上海橘粒科技",
			PhaseOrganized, CategoryKnowledge, "global", []string{"contract"}, "test",
		)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		return mem
	}

	t.Run("via_Write", func(t *testing.T) {
		s := tempSqliteStore(t)
		old := mkVersion(t, s, "42000")
		// Newer version of the same fact via Write — must supersede the old.
		_ = mkVersion(t, s, "54000")
		oldReloaded, err := s.FindByID(old.ID)
		if err != nil {
			t.Fatalf("reload old: %v", err)
		}
		if !IsSuperseded(oldReloaded) {
			t.Error("old version not marked superseded after Write() of newer version")
		}
	})

	t.Run("via_IngestMemory", func(t *testing.T) {
		s := tempSqliteStore(t)
		old := mkVersion(t, s, "42000")
		newer := &Memory{
			Content:   "瑞福莱暖通设备合同金额54000元，甲方上海橘粒科技",
			Phase:     PhaseOrganized,
			Category:  CategoryKnowledge,
			Scope:     "global",
			Source:    "test",
			Tags:      []string{"contract"},
			CreatedAt: time.Now(),
		}
		if err := s.IngestMemory(newer); err != nil {
			t.Fatalf("IngestMemory: %v", err)
		}
		oldReloaded, _ := s.FindByID(old.ID)
		if !IsSuperseded(oldReloaded) {
			t.Error("old version not marked superseded after IngestMemory() of newer version")
		}
	})

	t.Run("inbox_write_does_NOT_supersede", func(t *testing.T) {
		// Inbox writes are raw events, not facts — they must never supersede.
		s := tempSqliteStore(t)
		old := mkVersion(t, s, "42000")
		_, err := s.Write(
			"瑞福莱暖通设备合同金额54000元，甲方上海橘粒科技",
			PhaseInbox, CategoryInbox, "global", nil, "test",
		)
		if err != nil {
			t.Fatalf("inbox write: %v", err)
		}
		oldReloaded, _ := s.FindByID(old.ID)
		if IsSuperseded(oldReloaded) {
			t.Error("inbox write should NOT supersede an organized fact")
		}
	})
}

func TestResolveIDPrefix(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.Write("prefix matching test memory", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	fullID := mem.ID
	prefix := fullID[:8] // the truncated form shown by search/list

	// Read by prefix must return the same memory.
	got, err := s.Read(prefix)
	if err != nil {
		t.Fatalf("Read(prefix): %v", err)
	}
	if got.ID != fullID {
		t.Errorf("Read(prefix) returned id %q, want %q", got.ID, fullID)
	}

	// Full UUID still works.
	gotFull, err := s.Read(fullID)
	if err != nil {
		t.Fatalf("Read(full): %v", err)
	}
	if gotFull.ID != fullID {
		t.Errorf("Read(full) returned id %q, want %q", gotFull.ID, fullID)
	}

	// Delete by prefix must work.
	if err := s.Delete(prefix); err != nil {
		t.Fatalf("Delete(prefix): %v", err)
	}
	if _, err := s.Read(fullID); err == nil {
		t.Error("memory still readable after Delete(prefix)")
	}

	// Ambiguous prefix must error, not silently pick one.
	m2, _ := s.Write("another memory", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	_ = m2
	// Two UUIDs share no common long prefix, so use a deliberately ambiguous single char.
	if _, err := s.resolveID("-"); err == nil {
		t.Error("expected error for ambiguous prefix '-', got nil")
	}

	// Unknown prefix must error.
	if _, err := s.resolveID("nonexistent-id"); err == nil {
		t.Error("expected ErrNotFound for unknown prefix, got nil")
	}
}

func TestSqliteSearchLikeASCIIFast(t *testing.T) {
	s := tempSqliteStore(t)

	// Seed a corpus where "user" is common (matches many docs) — the worst case for the
	// old per-memory prefix loop, since non-matches triggered the expensive subquery path.
	for i := 0; i < 400; i++ {
		if i%3 == 0 {
			s.Write("the user prefers React and Next.js for frontend", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
		} else if i%3 == 1 {
			s.Write("memory system uses sqlite for storage", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
		} else {
			s.Write("design philosophy bans emoji", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
		}
	}

	start := time.Now()
	results, err := s.SearchLike(SearchOptions{Query: "user"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SearchLike: %v", err)
	}
	// "user" should match the ~133 React/frontend memories.
	if len(results) == 0 {
		t.Fatal("expected results for 'user', got 0")
	}
	// The regression would take many seconds (minutes at scale). A healthy run is sub-second.
	// 2s is a generous ceiling that still catches the N+1 blowup.
	if elapsed > 2*time.Second {
		t.Errorf("SearchLike('user') took %v on 400 memories — N+1 full-table-scan regression suspected (should be <2s)", elapsed)
	}
}

// TestSqliteSearchLikeCJKPrefix verifies CJK entity names still match via prefix expansion
// (the feature the slow loop was originally written for). A long entity name that doesn't
// appear verbatim must still surface memories containing its shorter form.
func TestSqliteSearchLikeCJKPrefix(t *testing.T) {
	s := tempSqliteStore(t)

	// Content contains the short form only.
	s.Write("瑞福莱暖通的服务合同金额5万4", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("unrelated project note", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	// Query the long form — must still match via the "瑞福莱" prefix.
	results, err := s.SearchLike(SearchOptions{Query: "瑞福莱暖通设备有限公司"})
	if err != nil {
		t.Fatalf("SearchLike: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected CJK prefix match for long entity name, got 0")
	}
	if !strings.Contains(results[0].Content, "瑞福莱") {
		t.Errorf("top result doesn't contain the entity prefix: %q", results[0].Content)
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

	mem, _ := s.WriteToInbox("upgrade me", CategoryInbox, "global", nil, "test", "", "")
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

	mem, _ := s.WriteToInbox("expiring soon", CategoryInbox, "global", nil, "test", "", "")
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

// rawContentSource returns (content, source) for a memory id by querying raw_entries.
func rawContentSource(t *testing.T, s *SqliteStore, id string) (string, bool) {
	t.Helper()
	var content string
	var rawID string
	err := s.db.QueryRow("SELECT content, raw_entry_id FROM memories WHERE id = ?", id).Scan(&content, &rawID)
	if err != nil {
		t.Fatalf("query memory: %v", err)
	}
	if rawID == "" {
		return "", false
	}
	var rawContent string
	err = s.db.QueryRow("SELECT content FROM raw_entries WHERE id = ?", rawID).Scan(&rawContent)
	if err != nil {
		t.Fatalf("query raw entry %s: %v", rawID, err)
	}
	return rawContent, true
}

func TestInsertMemoryRecordsRawEntry(t *testing.T) {
	s := tempSqliteStore(t)

	before, err := s.RawEntryCount()
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != 0 {
		t.Fatalf("raw_entries not empty at start: %d", before)
	}

	mem, err := s.WriteToInbox("a fragment to remember", CategoryInbox, "global", []string{"x"}, "claude", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// raw_entries must have captured the fragment
	after, err := s.RawEntryCount()
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 1 {
		t.Fatalf("raw_entries count = %d, want 1", after)
	}

	// the memory must link back to its raw entry
	got, ok := rawContentSource(t, s, mem.ID)
	if !ok {
		t.Fatal("memory has no raw_entry_id")
	}
	if got != "a fragment to remember" {
		t.Errorf("raw content = %q, want %q", got, "a fragment to remember")
	}
}

func TestRawEntryDedup(t *testing.T) {
	s := tempSqliteStore(t)

	// Same content written via two different paths must produce one raw entry.
	if _, err := s.WriteToInbox("duplicate content", CategoryInbox, "global", nil, "claude", "", ""); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := s.WriteToInbox("duplicate content", CategoryInbox, "global", nil, "manual", "", ""); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	count, err := s.RawEntryCount()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("raw_entries count = %d, want 1 (same content dedups by hash)", count)
	}
}

func TestDeletePreservesRawEntry(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.WriteToInbox("must survive deletion", CategoryInbox, "global", nil, "claude", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := s.Read(mem.ID); err != nil {
		t.Fatalf("read before delete: %v", err)
	}

	if err := s.Delete(mem.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// memory is gone...
	if _, err := s.Read(mem.ID); err == nil {
		t.Fatal("expected Read to fail after Delete")
	}

	// ...but the raw entry must still exist (the never-delete guarantee).
	count, err := s.RawEntryCount()
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 1 {
		t.Errorf("raw_entries count = %d, want 1 (raw entries must never be deleted)", count)
	}
}

// TestInitBackfillsRawEntries verifies that legacy memories (written before raw
// capture existed) are backfilled into raw_entries on init(), and that it is idempotent.
func TestInitBackfillsRawEntries(t *testing.T) {
	s := tempSqliteStore(t)

	// Simulate legacy data: insert memories directly, bypassing InsertMemory,
	// so raw_entries stays empty (the pre-fix state of the production DB).
	hash := HashContent("legacy memory content")
	_, err := s.db.Exec(`INSERT INTO memories (id, content, content_hash, phase, category, scope, source, created_at, updated_at)
		VALUES (?, ?, ?, 'inbox', 'inbox', 'global', 'manual', ?, ?)`,
		"legacy-1", "legacy memory content", hash, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed legacy memory: %v", err)
	}

	if c, _ := s.RawEntryCount(); c != 0 {
		t.Fatalf("raw_entries should be empty before backfill, got %d", c)
	}

	// init() is idempotent and safe to call again — this is the backfill trigger.
	if err := s.init(); err != nil {
		t.Fatalf("re-init: %v", err)
	}

	// raw_entries now captures the legacy memory.
	c, err := s.RawEntryCount()
	if err != nil {
		t.Fatalf("count after backfill: %v", err)
	}
	if c != 1 {
		t.Fatalf("raw_entries count = %d, want 1 after backfill", c)
	}

	// the legacy memory is linked back to its raw entry.
	var rawID string
	if err := s.db.QueryRow("SELECT raw_entry_id FROM memories WHERE id = ?", "legacy-1").Scan(&rawID); err != nil {
		t.Fatalf("query raw_entry_id: %v", err)
	}
	if rawID != hash {
		t.Errorf("raw_entry_id = %q, want %q", rawID, hash)
	}

	// Idempotent: a second init() must not duplicate or change anything.
	if err := s.init(); err != nil {
		t.Fatalf("third init: %v", err)
	}
	if c, _ := s.RawEntryCount(); c != 1 {
		t.Errorf("raw_entries count = %d after re-init, want 1 (backfill must be idempotent)", c)
	}
}

// TestInsertMemoryAddsKeywordTags verifies that InsertMemory auto-enriches tags
// with content-derived keywords, regardless of what tags the caller passed.
func TestInsertMemoryAddsKeywordTags(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.WriteToInbox("We need state-management for the frontend using react and golang", CategoryInbox, "global", []string{"user-tag"}, "manual", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// caller-provided tag preserved
	if !contains(got.Tags, "user-tag") {
		t.Errorf("caller tag 'user-tag' missing from %v", got.Tags)
	}
	// keywords auto-added
	if !contains(got.Tags, "state-management") {
		t.Errorf("keyword 'state-management' missing from %v", got.Tags)
	}
	if !contains(got.Tags, "react") {
		t.Errorf("keyword 'react' missing from %v", got.Tags)
	}
}

// TestInsertMemoryAutoCategorizes verifies that InsertMemory assigns a content-derived
// category when none was given (default inbox), and preserves an explicit category.
func TestInsertMemoryAutoCategorizes(t *testing.T) {
	s := tempSqliteStore(t)

	// preference cue → preferences (read from DB; returned struct keeps the input category)
	mem, err := s.WriteToInbox("I prefer dark theme for the code editor", CategoryInbox, "global", nil, "manual", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := s.Read(mem.ID)
	if got.Category != CategoryPreferences {
		t.Errorf("preference content category = %q, want preferences", got.Category)
	}

	// technical content → knowledge
	mem2, _ := s.WriteToInbox("the golang api returns json over http", CategoryInbox, "global", nil, "manual", "", "")
	got2, _ := s.Read(mem2.ID)
	if got2.Category != CategoryKnowledge {
		t.Errorf("technical content category = %q, want knowledge", got2.Category)
	}

	// explicit category preserved
	mem3, _ := s.Write("explicit", PhaseOrganized, CategoryPeople, "global", nil, "manual")
	got3, _ := s.Read(mem3.ID)
	if got3.Category != CategoryPeople {
		t.Errorf("explicit category = %q, want people (must be preserved)", got3.Category)
	}
}

// TestInsertMemoryProjectPersistence verifies the Project field is persisted and filterable.
func TestInsertMemoryProjectPersistence(t *testing.T) {
	s := tempSqliteStore(t)

	if _, err := s.WriteToInbox("makro feature note", CategoryInbox, "global", nil, "conversations", "makro", ""); err != nil {
		t.Fatalf("write makro: %v", err)
	}
	if _, err := s.WriteToInbox("fingersaver chat", CategoryInbox, "global", nil, "fingersaver", "fingersaver", ""); err != nil {
		t.Fatalf("write fingersaver: %v", err)
	}
	if _, err := s.WriteToInbox("no project context", CategoryInbox, "global", nil, "manual", "", ""); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	proj, _ := s.List(ListOptions{Project: "makro"})
	if len(proj) != 1 {
		t.Errorf("project=makro filter = %d memories, want 1", len(proj))
	}
	proj, _ = s.List(ListOptions{Project: "fingersaver"})
	if len(proj) != 1 {
		t.Errorf("project=fingersaver filter = %d memories, want 1", len(proj))
	}
}

// TestMarkConsumedAtomicAndIdempotent verifies the bitmask consumption tracking:
// marking is idempotent (same consumer twice = one bit), distinct consumers each set their bit,
// and the result is queryable via IsConsumed.
func TestMarkConsumedAtomicAndIdempotent(t *testing.T) {
	s := tempSqliteStore(t)
	mem, err := s.WriteToInbox("consume me", CategoryInbox, "global", nil, "test", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// idempotent: same consumer twice → one bit
	if err := s.MarkConsumed(mem.ID, "consolidate-llm"); err != nil {
		t.Fatalf("mark 1: %v", err)
	}
	if err := s.MarkConsumed(mem.ID, "consolidate-llm"); err != nil {
		t.Fatalf("mark 2: %v", err)
	}

	// second distinct consumer
	if err := s.MarkConsumed(mem.ID, "enrich-tags"); err != nil {
		t.Fatalf("mark enrich: %v", err)
	}

	got, _ := s.FindByID(mem.ID)
	if got.ConsumedMask != int64(ConsumerConsolidateLLM|ConsumerEnrichTags) {
		t.Errorf("consumed_mask = %d, want %d (both bits)", got.ConsumedMask, int64(ConsumerConsolidateLLM|ConsumerEnrichTags))
	}
	if !IsConsumed(got.ConsumedMask, "consolidate-llm") || !IsConsumed(got.ConsumedMask, "enrich-tags") {
		t.Errorf("expected both consumers consumed, mask=%d", got.ConsumedMask)
	}
	if IsConsumed(got.ConsumedMask, "fact-processor") {
		t.Error("fact-processor should not be consumed")
	}

	// unknown consumer is a no-op (no bit set, no error)
	if err := s.MarkConsumed(mem.ID, "no-such-processor"); err != nil {
		t.Errorf("unknown consumer should be no-op, got err: %v", err)
	}
	got2, _ := s.FindByID(mem.ID)
	if got2.ConsumedMask != got.ConsumedMask {
		t.Errorf("unknown consumer changed mask: %d -> %d", got.ConsumedMask, got2.ConsumedMask)
	}
}

// TestMarkConsumedConcurrent verifies that concurrent consumers on the same memory
// do not lose each other's marks (the race the old read-modify-write had).
func TestMarkConsumedConcurrent(t *testing.T) {
	s := tempSqliteStore(t)
	mem, err := s.WriteToInbox("concurrent consume", CategoryInbox, "global", nil, "test", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// Two consumers mark concurrently, many times each — under the old read-modify-write
	// this would intermittently drop one consumer's bit. The atomic bitmask never does.
	consumers := []string{"fact-processor", "consolidate-llm", "enrich-tags"}
	done := make(chan struct{}, len(consumers))
	for _, c := range consumers {
		go func(name string) {
			for i := 0; i < 50; i++ {
				s.MarkConsumed(mem.ID, name)
			}
			done <- struct{}{}
		}(c)
	}
	for range consumers {
		<-done
	}

	got, _ := s.FindByID(mem.ID)
	want := int64(ConsumerFactProcessor | ConsumerConsolidateLLM | ConsumerEnrichTags)
	if got.ConsumedMask != want {
		t.Errorf("after concurrent marks, consumed_mask = %d, want %d (a consumer's bit was lost — race)", got.ConsumedMask, want)
	}
}

// TestBuildTrigramMatch unit-tests the MATCH expression builder. Trigram can only match
// substrings of ≥3 codepoints, so any shorter keyword must force a SearchLike fallback.
func TestBuildTrigramMatch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", `"hello"`},                          // single ASCII token
		{"橘粒科技", `"橘粒科技"`},                        // single CJK token
		{"橘粒科技 网站开发", `"橘粒科技" OR "网站开发"`},        // multi-word, all ≥3 runes
		{"a OR b", ``},                                // OR-syntax split, both <3 → fallback
		{"合同 报价", ``},                                // 2-rune CJK keywords → fallback
		{"hello 合同", ``},                              // mixed long/short → fallback (no silent drop)
		{"say \"hi\"", `"say" OR """hi"""`},           // both split keywords ≥3 runes (quotes count)
		{`say "hello"`, `"say" OR """hello"""`},       // embedded quotes escaped per-keyword
		{"", ``},                                      // empty
		{"   ", ``},                                   // whitespace only
	}
	for _, c := range cases {
		if got := buildTrigramMatch(c.in); got != c.want {
			t.Errorf("buildTrigramMatch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSearchCJKViaFTS verifies CJK queries now hit the trigram FTS index (previously the
// unicode61 tokenizer couldn't segment CJK, so anything but a single ASCII token fell back
// to SearchLike's full-table LIKE scan). All keywords here are ≥3 runes → FTS path.
func TestSearchCJKViaFTS(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("橘粒科技和瑞福莱暖通签订网站开发合同", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("项目技术架构采用 React + Next.js", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	// Single CJK token — substring anywhere in content, via trigram inverted index.
	results, err := s.Search(SearchOptions{Query: "瑞福莱暖通"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "瑞福莱") {
		t.Errorf("single CJK token via FTS: got %d results", len(results))
	}

	// Multi-word CJK query where every keyword is ≥3 runes → also the FTS path now.
	results, err = s.Search(SearchOptions{Query: "橘粒科技 网站开发"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "橘粒") {
		t.Errorf("multi-word CJK via FTS: got %d results", len(results))
	}
}

// TestSearchShortCJKKeywordFallback: trigram can't match <3-rune keywords (e.g. "合同"),
// so such queries must fall back to SearchLike and still return correct results.
func TestSearchShortCJKKeywordFallback(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("橘粒科技和瑞福莱暖通签订网站开发合同，报价5万4", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("unrelated note about weather", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	results, err := s.Search(SearchOptions{Query: "橘粒科技 合同 报价"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("short-keyword fallback returned 0 results — SearchLike fallback broken")
	}
	if !strings.Contains(results[0].Content, "合同") {
		t.Errorf("top result should be the contract memory, got %q", results[0].Content)
	}
}

// TestSearchHybridStrategy exercises the hybrid RRF fusion with the new trigram dispatch:
// SearchLike must receive the RAW query (not the MATCH expression) or keyword splitting breaks.
func TestSearchHybridStrategy(t *testing.T) {
	s := tempSqliteStore(t)
	s.searchStrategy = "hybrid"

	s.Write("橘粒科技和瑞福莱暖通签订网站开发合同，报价5万4", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.Write("dark mode preference", PhaseOrganized, CategoryPreferences, "global", nil, "test")

	results, err := s.Search(SearchOptions{Query: "橘粒科技 报价"})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("hybrid strategy returned 0 results — SearchLike likely received the MATCH expression")
	}

	results, err = s.Search(SearchOptions{Query: "dark"})
	if err != nil {
		t.Fatalf("hybrid ascii search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("hybrid ascii results = %d, want 1", len(results))
	}
}

// TestMigrateFTSTokenizer simulates an old database whose memories_fts still uses the
// unicode61 tokenizer: on reopen, migrateFTSTokenizer must rebuild it with trigram and
// reindex content+tags from the source tables.
func TestMigrateFTSTokenizer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSqliteStore(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Write("橘粒科技和瑞福莱暖通签订网站开发合同", PhaseOrganized, CategoryKnowledge, "global", []string{"签约"}, "test")
	s.Close()

	// Downgrade to the old tokenizer, simulating a pre-migration database.
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("reopen raw: %v", err)
		}
		db.Exec("DROP TABLE memories_fts")
		db.Exec(`CREATE VIRTUAL TABLE memories_fts USING fts5(
			memory_id UNINDEXED, content, tags, scope, source, tokenize='unicode61')`)
		db.Exec(`INSERT INTO memories_fts (memory_id, content, tags, scope, source)
			SELECT m.id, m.content,
			       COALESCE((SELECT group_concat(t.tag, ' ') FROM tags t WHERE t.memory_id = m.id), ''),
			       m.scope, m.source
			FROM memories m`)
		db.Close()
	}

	// Reopen through the normal path — migration should fire and rebuild with trigram.
	s2, err := NewSqliteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	var createSQL string
	if err := s2.DB().QueryRow(
		"SELECT sql FROM sqlite_master WHERE name='memories_fts'").Scan(&createSQL); err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if !strings.Contains(createSQL, "trigram") {
		t.Errorf("memories_fts still not trigram: %q", createSQL)
	}

	// Reindexed content must be searchable via the FTS path (single CJK token, ≥3 runes).
	results, err := s2.Search(SearchOptions{Query: "瑞福莱暖通"})
	if err != nil {
		t.Fatalf("search after migration: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("post-migration search results = %d, want 1 (backfill lost rows?)", len(results))
	}

	// Migration must be idempotent — a second reopen is a no-op (still trigram, still searchable).
	s2.Close()
	s3, err := NewSqliteStore(dbPath)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer s3.Close()
	if results, _ := s3.Search(SearchOptions{Query: "瑞福莱暖通"}); len(results) != 1 {
		t.Errorf("results after idempotent re-migration = %d, want 1", len(results))
	}
}

// TestIngestMemoryValidationGate: the command gate (DDIA ch3) rejects garbage BEFORE the
// event is appended — raw_entries must not grow on rejected writes.
func TestIngestMemoryValidationGate(t *testing.T) {
	s := tempSqliteStore(t)

	rejected := []struct {
		name string
		mem  *Memory
	}{
		{"empty content", &Memory{Content: "", Phase: PhaseInbox}},
		{"whitespace only", &Memory{Content: "   \n\t  ", Phase: PhaseInbox}},
		{"oversize", &Memory{Content: strings.Repeat("x", MaxContentLen+1), Phase: PhaseInbox}},
		{"invalid phase", &Memory{Content: "ok content", Phase: Phase("bogus")}},
	}
	for _, r := range rejected {
		if err := s.IngestMemory(r.mem); err == nil {
			t.Errorf("%s: expected rejection, got nil", r.name)
		}
	}
	count, _ := s.RawEntryCount()
	if count != 0 {
		t.Errorf("raw_entries = %d after rejected writes, want 0 (garbage must not become permanent events)", count)
	}

	// Normalization: invalid category falls back to inbox (auto-categorized later);
	// content is trimmed before hashing.
	mem := &Memory{Content: "  用户偏好深色模式  ", Phase: PhaseInbox, Category: Category("not-a-cat"), Tags: []string{" ui ", "", "ui"}}
	if err := s.IngestMemory(mem); err != nil {
		t.Fatalf("valid write rejected: %v", err)
	}
	if mem.Content != "用户偏好深色模式" {
		t.Errorf("content not trimmed: %q", mem.Content)
	}
	got, _ := s.FindByID(mem.ID)
	if got.Category == Category("not-a-cat") {
		t.Errorf("invalid category persisted: %q", got.Category)
	}
	if len(got.Tags) != len(normalizeTags(got.Tags)) {
		t.Errorf("tags not normalized: %v", got.Tags)
	}
}

// TestRawEntryCarriesProvenance: the event log is self-contained (fat events) — provenance
// is captured on first append, and a dedup hit backfills provenance it lacks without
// creating a second event.
func TestRawEntryCarriesProvenance(t *testing.T) {
	s := tempSqliteStore(t)

	first := &Memory{Content: "瑞福莱暖通签订合同", Phase: PhaseInbox, Source: "claude", Project: "memory-cli"}
	if err := s.IngestMemory(first); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	// Same content again, now with richer provenance — must NOT create a second event.
	second := &Memory{Content: "瑞福莱暖通签订合同", Phase: PhaseInbox, Source: "claude",
		Project: "memory-cli", TmuxSession: "work", GitBranch: "feat/cqrs", PromptID: "p-42"}
	if err := s.IngestMemory(second); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	count, _ := s.RawEntryCount()
	if count != 1 {
		t.Fatalf("raw_entries = %d, want 1 (dedup must not add events)", count)
	}

	entries, err := s.ListRawEntries()
	if err != nil {
		t.Fatalf("ListRawEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Project != "memory-cli" || e.TmuxSession != "work" || e.GitBranch != "feat/cqrs" || e.PromptID != "p-42" {
		t.Errorf("event provenance incomplete: %+v", e)
	}
}

// TestRebuildIndexes: safe rebuild restores the FTS index after damage.
func TestRebuildIndexes(t *testing.T) {
	s := tempSqliteStore(t)
	s.Write("瑞福莱暖通的网站开发合同", PhaseOrganized, CategoryKnowledge, "global", nil, "test")

	// Simulate index damage: wipe the FTS table.
	s.db.Exec("DELETE FROM memories_fts")
	if r, _ := s.Search(SearchOptions{Query: "瑞福莱暖通"}); len(r) != 0 {
		t.Fatalf("precondition failed: expected 0 results with wiped FTS")
	}

	stats, err := s.RebuildIndexes()
	if err != nil {
		t.Fatalf("RebuildIndexes: %v", err)
	}
	if stats.FTSRows != 1 {
		t.Errorf("FTSRows = %d, want 1", stats.FTSRows)
	}
	if r, _ := s.Search(SearchOptions{Query: "瑞福莱暖通"}); len(r) != 1 {
		t.Errorf("search after rebuild = %d results, want 1", len(r))
	}
}

// TestRebuildFromEvents: full replay wipes the derived layer and rebuilds it from the
// event log — content, provenance and searchability survive; phase resets to inbox.
func TestRebuildFromEvents(t *testing.T) {
	s := tempSqliteStore(t)

	s.Write("瑞福莱暖通的网站开发合同", PhaseOrganized, CategoryKnowledge, "global", nil, "test")
	s.IngestMemory(&Memory{Content: "用户偏好深色主题", Phase: PhaseInbox, Source: "claude", Project: "proj-a", PromptID: "p-7"})

	before, _ := s.ListRawEntries()
	if len(before) != 2 {
		t.Fatalf("seed events = %d, want 2", len(before))
	}

	stats, err := s.RebuildFromEvents()
	if err != nil {
		t.Fatalf("RebuildFromEvents: %v", err)
	}
	if stats.Rebuilt != 2 || stats.Skipped != 0 {
		t.Errorf("rebuilt=%d skipped=%d, want 2/0", stats.Rebuilt, stats.Skipped)
	}

	// Searchability survives.
	if r, _ := s.Search(SearchOptions{Query: "瑞福莱暖通"}); len(r) != 1 {
		t.Errorf("CJK search after full rebuild = %d results, want 1", len(r))
	}
	// Provenance survives replay.
	if r, _ := s.Search(SearchOptions{Query: "用户偏好"}); len(r) != 1 {
		t.Fatalf("search for provenance-carrying memory failed")
	} else if r[0].Project != "proj-a" || r[0].PromptID != "p-7" {
		t.Errorf("provenance lost in replay: project=%q prompt=%q", r[0].Project, r[0].PromptID)
	}
	// Phase resets to inbox — daemon re-derives processing state.
	all, _ := s.List(ListOptions{})
	for _, m := range all {
		if m.Phase != PhaseInbox {
			t.Errorf("replayed memory phase = %q, want inbox", m.Phase)
		}
	}
	// The event log itself is untouched.
	after, _ := s.ListRawEntries()
	if len(after) != 2 {
		t.Errorf("events after rebuild = %d, want 2 (log must be untouched)", len(after))
	}
}

// TestRawEntryIDTraceability: every read path surfaces raw_entry_id, closing the
// traceability chain from a derived memory back to its source event.
func TestRawEntryIDTraceability(t *testing.T) {
	s := tempSqliteStore(t)

	mem, err := s.WriteToInbox("追溯链验证内容", CategoryInbox, "global", nil, "test", "proj-t", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if mem.RawEntryID == "" {
		t.Fatal("returned memory lacks RawEntryID (caller-side chain broken)")
	}

	// Read paths surface it too.
	got, err := s.FindByID(mem.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.RawEntryID != mem.RawEntryID {
		t.Errorf("FindByID RawEntryID = %q, want %q", got.RawEntryID, mem.RawEntryID)
	}
	results, _ := s.Search(SearchOptions{Query: "追溯链验证"})
	if len(results) != 1 || results[0].RawEntryID != mem.RawEntryID {
		t.Errorf("Search RawEntryID mismatch: %+v", results)
	}

	// The event it points to actually exists in the log.
	entries, _ := s.ListRawEntries()
	found := false
	for _, e := range entries {
		if e.ID == mem.RawEntryID && e.Content == "追溯链验证内容" && e.Project == "proj-t" {
			found = true
		}
	}
	if !found {
		t.Error("RawEntryID does not resolve to an event in the log")
	}
}

// TestSessionsWithUnconsumed: the session-digest backlog groups by session_id and
// disappears once the consumer bit is marked.
func TestSessionsWithUnconsumed(t *testing.T) {
	s := tempSqliteStore(t)

	s.IngestMemory(&Memory{Content: "sess-a 第一条", Phase: PhaseInbox, SessionID: "sess-a", Project: "pa"})
	s.IngestMemory(&Memory{Content: "sess-a 第二条", Phase: PhaseInbox, SessionID: "sess-a", Project: "pa"})
	s.IngestMemory(&Memory{Content: "sess-b 唯一", Phase: PhaseInbox, SessionID: "sess-b", Project: "pb"})
	s.IngestMemory(&Memory{Content: "无会话写入", Phase: PhaseInbox, SessionID: ""})

	refs, err := s.SessionsWithUnconsumed("session-digest", 10)
	if err != nil {
		t.Fatalf("SessionsWithUnconsumed: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("sessions = %d, want 2 (no-session writes excluded)", len(refs))
	}
	if refs[0].SessionID != "sess-a" || refs[0].UnconsumedCount != 2 || refs[0].Project != "pa" {
		t.Errorf("first ref wrong: %+v", refs[0])
	}

	// Mark consumed → no longer pending.
	for _, m := range mustListBySession(t, s, "sess-a") {
		s.MarkConsumed(m.ID, "session-digest")
	}
	refs, _ = s.SessionsWithUnconsumed("session-digest", 10)
	if len(refs) != 1 || refs[0].SessionID != "sess-b" {
		t.Errorf("after marking sess-a: %+v", refs)
	}
}

func mustListBySession(t *testing.T, s *SqliteStore, sid string) []*Memory {
	t.Helper()
	memories, err := s.ListMemoriesBySession(sid, 0)
	if err != nil {
		t.Fatalf("ListMemoriesBySession: %v", err)
	}
	return memories
}

// TestSessionViewUpsertAndFilter: digests upsert per session and filter by project/entity.
func TestSessionViewUpsertAndFilter(t *testing.T) {
	s := tempSqliteStore(t)

	v1 := &SessionView{SessionID: "s1", Project: "zcode", Entity: "瑞福莱", Facet: "cases",
		Task: "优化 Cases 页", Summary: "完成了重构", Lessons: `["ICP备案要先查主体"]`, MemoryCount: 5}
	if err := s.UpsertSessionView(v1); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Re-digest replaces the row wholesale.
	v2 := &SessionView{SessionID: "s1", Project: "zcode", Entity: "瑞福莱", Facet: "product",
		Task: "继续产品页", Summary: "更新完成", MemoryCount: 8}
	s.UpsertSessionView(v2)
	s.UpsertSessionView(&SessionView{SessionID: "s2", Project: "juli", Entity: "juli", Task: "数据流优化", Summary: "完成"})

	got, _ := s.ListSessionViews(SessionViewFilter{})
	if len(got) != 2 || got[0].MemoryCount != 8 || got[0].Facet != "product" {
		t.Errorf("upsert semantics wrong: %+v", got)
	}
	if v, _ := s.ListSessionViews(SessionViewFilter{Entity: "瑞福莱"}); len(v) != 1 || v[0].SessionID != "s1" {
		t.Errorf("entity filter: %+v", v)
	}
	if v, _ := s.ListSessionViews(SessionViewFilter{Project: "juli"}); len(v) != 1 || v[0].SessionID != "s2" {
		t.Errorf("project filter: %+v", v)
	}
}

// TestProjectStateSetGetStale: the shared-state projection — upsert, history append,
// staleness computed at read time, and the state.md bootstrap file.
func TestProjectStateSetGetStale(t *testing.T) {
	s := tempSqliteStore(t)

	ps, err := s.SetProjectState(StateInput{
		Project: "ruifulai", Version: "v26", Branch: "main", Commit: "a1b2c3d4e5f6",
		Phase: "开发", Blockers: []string{"图钉回归未跑", " ", "客户确认"}, NextActions: []string{"锚点收尾"},
		Notes: "给下一个 agent:先跑回归", UpdatedBy: "zcode/sess-a",
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(ps.Blockers) != 2 || ps.Stale {
		t.Errorf("normalize/stale wrong: %+v", ps)
	}

	got, err := s.GetProjectState("ruifulai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CommitShort() != "a1b2c3d" || got.Notes != "给下一个 agent:先跑回归" {
		t.Errorf("roundtrip wrong: %+v", got)
	}
	if got.Stale || got.AgeHours > 1 {
		t.Errorf("fresh state flagged stale: %+v", got)
	}

	// Re-set replaces wholesale (LWW handoff) and appends history.
	if _, err := s.SetProjectState(StateInput{Project: "ruifulai", Version: "v27", Commit: "fff000"}); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	got, _ = s.GetProjectState("ruifulai")
	if got.Version != "v27" || got.Notes != "" {
		t.Errorf("LWW replace wrong: %+v", got)
	}
	hist, _ := s.StateHistory("ruifulai", 10)
	if len(hist) != 2 || hist[0].Version != "v27" {
		t.Errorf("history wrong: %d entries", len(hist))
	}

	// Validation gate.
	if _, err := s.SetProjectState(StateInput{Project: "  "}); err == nil {
		t.Error("empty project accepted")
	}

	// state.md bootstrap file lands next to the DB.
	md, err := os.ReadFile(s.stateMarkdownPath())
	if err != nil || !strings.Contains(string(md), "ruifulai") || !strings.Contains(string(md), "v27") {
		t.Errorf("state.md wrong: %v %q", err, string(md)[:min(80, len(string(md)))])
	}
}

// TestGraduationQueue: business facts enqueue, surface in state.md, and archive with a
// pointer (memory never duplicates business data).
func TestGraduationQueue(t *testing.T) {
	s := tempSqliteStore(t)

	g1, err := s.AddGraduation("ruifulai", "客户确认 v26 验收通过", "sess-A")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	s.AddGraduation("ruifulai", "  ", "sess-A") // invalid: empty fact
	if grads, _ := s.ListGraduations(true); len(grads) != 1 {
		t.Fatalf("pending = %d, want 1 (empty fact rejected)", len(grads))
	}

	// state.md surfaces the queue at session start.
	s.SetProjectState(StateInput{Project: "ruifulai", Version: "v26"})
	md, _ := os.ReadFile(s.stateMarkdownPath())
	if !strings.Contains(string(md), "客户确认 v26") {
		t.Error("state.md missing pending graduation")
	}

	// Archiving requires a pointer and drains the queue.
	if err := s.CompleteGraduation(g1.ID, ""); err == nil {
		t.Error("archive without pointer accepted")
	}
	if err := s.CompleteGraduation(g1.ID, "pb://ruifulai/feedback/rec123"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := s.CompleteGraduation(g1.ID, "pb://again"); err == nil {
		t.Error("double archive accepted")
	}
	if grads, _ := s.ListGraduations(true); len(grads) != 0 {
		t.Errorf("pending after archive = %d, want 0", len(grads))
	}
	all, _ := s.ListGraduations(false)
	if len(all) != 1 || all[0].PBPointer != "pb://ruifulai/feedback/rec123" {
		t.Errorf("archived entry lost pointer: %+v", all)
	}
	md, _ = os.ReadFile(s.stateMarkdownPath())
	if strings.Contains(string(md), "客户确认 v26") {
		t.Error("state.md still shows archived graduation")
	}
}

// TestRebuildSkipsNoiseEvents: thinking/notification events survive in the log as history
// but never re-enter the derived layer on replay.
func TestRebuildSkipsNoiseEvents(t *testing.T) {
	s := tempSqliteStore(t)

	s.IngestMemory(&Memory{Content: "真实工作事实:瑞福莱 v26 部署完成", Phase: PhaseInbox, SessionID: "s1"})
	s.IngestMemory(&Memory{Content: "[thinking] 这段是模型内心独白,不该进视图", Phase: PhaseInbox, SessionID: "s1"})
	s.IngestMemory(&Memory{Content: "Q: <task-notification><task-id>x</task-id></task-notification>", Phase: PhaseInbox, SessionID: "s1"})

	if !isNoiseEvent("[thinking] x") || !isNoiseEvent("Q: <task-notification> y") {
		t.Fatal("isNoiseEvent misjudges")
	}
	if isNoiseEvent("包含 [thinking] 字样但有实际内容的正文") {
		t.Fatal("isNoiseEvent over-filters real content")
	}

	stats, err := s.RebuildFromEvents()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if stats.Rebuilt != 1 || stats.Skipped != 2 {
		t.Errorf("rebuilt=%d skipped=%d, want 1/2", stats.Rebuilt, stats.Skipped)
	}
	all, _ := s.List(ListOptions{})
	if len(all) != 1 {
		t.Errorf("derived layer has %d memories, want 1 (noise leaked)", len(all))
	}
	if n, _ := s.RawEntryCount(); n != 3 {
		t.Errorf("events = %d, want 3 (log must keep noise as history)", n)
	}
}
