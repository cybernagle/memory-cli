package store

import (
	"path/filepath"
	"strings"
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

	mem, err := s.WriteToInbox("inbox item", "global", []string{"tag1"}, "test", "", "")
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

	mem, _ := s.WriteToInbox("upgrade me", "global", nil, "test", "", "")
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

	mem, _ := s.WriteToInbox("expiring soon", "global", nil, "test", "", "")
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

	mem, err := s.WriteToInbox("a fragment to remember", "global", []string{"x"}, "claude", "", "")
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
	if _, err := s.WriteToInbox("duplicate content", "global", nil, "claude", "", ""); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := s.WriteToInbox("duplicate content", "global", nil, "manual", "", ""); err != nil {
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

	mem, err := s.WriteToInbox("must survive deletion", "global", nil, "claude", "", "")
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

	mem, err := s.WriteToInbox("We need state-management for the frontend using react and golang", "global", []string{"user-tag"}, "manual", "", "")
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
	mem, err := s.WriteToInbox("I prefer dark theme for the code editor", "global", nil, "manual", "", "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := s.Read(mem.ID)
	if got.Category != CategoryPreferences {
		t.Errorf("preference content category = %q, want preferences", got.Category)
	}

	// technical content → knowledge
	mem2, _ := s.WriteToInbox("the golang api returns json over http", "global", nil, "manual", "", "")
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

	if _, err := s.WriteToInbox("makro feature note", "global", nil, "conversations", "makro", ""); err != nil {
		t.Fatalf("write makro: %v", err)
	}
	if _, err := s.WriteToInbox("fingersaver chat", "global", nil, "fingersaver", "fingersaver", ""); err != nil {
		t.Fatalf("write fingersaver: %v", err)
	}
	if _, err := s.WriteToInbox("no project context", "global", nil, "manual", "", ""); err != nil {
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
	mem, err := s.WriteToInbox("consume me", "global", nil, "test", "", "")
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
	mem, err := s.WriteToInbox("concurrent consume", "global", nil, "test", "", "")
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
