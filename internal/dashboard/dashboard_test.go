package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func setupServer(t *testing.T) (*Server, store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: dir, ShortTermTTL: "24h"},
	}
	s := store.New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return NewServer(s, nil), s
}

func mustWrite(t *testing.T, s store.Store, content string, phase store.Phase, cat store.Category, tags []string) string {
	t.Helper()
	mem, err := s.Write(content, phase, cat, "global", tags, "test")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return mem.ID
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}

func parseJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("parse json: %v", err)
	}
}

func TestStatsEmpty(t *testing.T) {
	srv, _ := setupServer(t)
	w := get(t, srv, "/api/stats")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp StatsResponse
	parseJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
}

func TestStatsWithMemories(t *testing.T) {
	srv, s := setupServer(t)
	mustWrite(t, s, "mem1", store.PhaseInbox, store.CategoryKnowledge, []string{"go"})
	mustWrite(t, s, "mem2", store.PhaseOrganized, store.CategoryPeople, []string{"go", "team"})
	mustWrite(t, s, "mem3", store.PhaseOrganized, store.CategoryKnowledge, []string{"go"})

	w := get(t, srv, "/api/stats")
	var resp StatsResponse
	parseJSON(t, w.Body.Bytes(), &resp)

	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
	if resp.Inbox != 1 {
		t.Errorf("inbox = %d, want 1", resp.Inbox)
	}
	if resp.Organized != 2 {
		t.Errorf("organized = %d, want 2", resp.Organized)
	}
	if resp.Categories["knowledge"] != 2 {
		t.Errorf("knowledge = %d, want 2", resp.Categories["knowledge"])
	}
	if resp.Tags["go"] != 3 {
		t.Errorf("tag go = %d, want 3", resp.Tags["go"])
	}
}

func TestHandleMemoriesFilter(t *testing.T) {
	srv, s := setupServer(t)
	mustWrite(t, s, "inbox mem", store.PhaseInbox, store.CategoryInbox, nil)
	mustWrite(t, s, "organized mem", store.PhaseOrganized, store.CategoryKnowledge, nil)

	w := get(t, srv, "/api/memories?phase=organized")
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	memories := resp["memories"].([]any)
	if len(memories) != 1 {
		t.Errorf("memories = %d, want 1", len(memories))
	}
}

func TestHandleMemoriesCategoryFilter(t *testing.T) {
	srv, s := setupServer(t)
	mustWrite(t, s, "knowledge mem", store.PhaseOrganized, store.CategoryKnowledge, nil)
	mustWrite(t, s, "people mem", store.PhaseOrganized, store.CategoryPeople, nil)

	w := get(t, srv, "/api/memories?category=people")
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	memories := resp["memories"].([]any)
	if len(memories) != 1 {
		t.Errorf("memories = %d, want 1", len(memories))
	}
}

func TestHandleMemoryDetail(t *testing.T) {
	srv, s := setupServer(t)
	id := mustWrite(t, s, "test content", store.PhaseOrganized, store.CategoryKnowledge, []string{"test"})

	w := get(t, srv, "/api/memories/"+id)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var mem store.Memory
	parseJSON(t, w.Body.Bytes(), &mem)
	if mem.Content != "test content" {
		t.Errorf("content = %q", mem.Content)
	}
}

func TestHandleMemoryDetailNotFound(t *testing.T) {
	srv, _ := setupServer(t)
	w := get(t, srv, "/api/memories/nonexistent")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleSearch(t *testing.T) {
	srv, s := setupServer(t)
	mustWrite(t, s, "dark mode preference", store.PhaseOrganized, store.CategoryPreferences, nil)
	mustWrite(t, s, "light mode default", store.PhaseOrganized, store.CategoryPreferences, nil)

	w := get(t, srv, "/api/search?q=dark")
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	results := resp["results"].([]any)
	if len(results) != 1 {
		t.Errorf("results = %d, want 1", len(results))
	}
}

func TestHandleSearchMissingQuery(t *testing.T) {
	srv, _ := setupServer(t)
	w := get(t, srv, "/api/search")
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleLinks(t *testing.T) {
	srv, s := setupServer(t)
	id1 := mustWrite(t, s, "memory one", store.PhaseOrganized, store.CategoryKnowledge, nil)
	id2 := mustWrite(t, s, "memory two", store.PhaseOrganized, store.CategoryKnowledge, nil)
	s.LinkMemories(id1, id2)

	w := get(t, srv, "/api/memories/"+id1+"/links")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	fwd := resp["forward"].([]any)
	back := resp["backlinks"].([]any)
	if len(fwd) != 1 {
		t.Errorf("forward links = %d, want 1", len(fwd))
	}
	if len(back) != 1 {
		t.Errorf("backlinks = %d, want 1", len(back))
	}
}

func TestHandleGraph(t *testing.T) {
	srv, s := setupServer(t)
	id1 := mustWrite(t, s, "node one", store.PhaseOrganized, store.CategoryKnowledge, nil)
	id2 := mustWrite(t, s, "node two", store.PhaseOrganized, store.CategoryPeople, nil)
	s.LinkMemories(id1, id2)

	w := get(t, srv, "/api/graph")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	nodes := resp["nodes"].([]any)
	edges := resp["edges"].([]any)
	if len(nodes) != 2 {
		t.Errorf("nodes = %d, want 2", len(nodes))
	}
	if len(edges) != 1 {
		t.Errorf("edges = %d, want 1", len(edges))
	}
}

func TestHandleIndex(t *testing.T) {
	srv, _ := setupServer(t)
	w := get(t, srv, "/")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
}

func TestHandleNotFound(t *testing.T) {
	srv, _ := setupServer(t)
	w := get(t, srv, "/nonexistent")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestComputeStatsWithExpiring(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Storage: config.StorageConfig{Root: dir, ShortTermTTL: "1h"}}
	s := store.New(cfg)
	s.Init()

	s.Write("expiring soon", store.PhaseInbox, store.CategoryInbox, "global", nil, "test")

	stats, err := ComputeStats(s)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if stats.Total != 1 {
		t.Errorf("total = %d, want 1", stats.Total)
	}
	if stats.ExpiringSoon != 1 {
		t.Errorf("expiring_soon = %d, want 1", stats.ExpiringSoon)
	}
}

func TestRecent24h(t *testing.T) {
	srv, s := setupServer(t)
	mustWrite(t, s, "recent", store.PhaseOrganized, store.CategoryKnowledge, nil)

	w := get(t, srv, "/api/memories?recent=true")
	var resp map[string]any
	parseJSON(t, w.Body.Bytes(), &resp)
	memories := resp["memories"].([]any)
	if len(memories) != 1 {
		t.Errorf("recent = %d, want 1", len(memories))
	}
}
