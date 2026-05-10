package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func setupHTTPServer(t *testing.T, keys []string) *HTTPServer {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: dir, ShortTermTTL: "24h"},
	}
	s := store.New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return NewHTTPServer(keys, s)
}

func TestHTTPMissingAuth(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})
	req := httptest.NewRequest("POST", "/a2a", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHTTPInvalidKey(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})
	req := httptest.NewRequest("POST", "/a2a", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHTTPInvalidScheme(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})
	req := httptest.NewRequest("POST", "/a2a", nil)
	req.Header.Set("Authorization", "Basic test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHTTPWriteAndList(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	// Write a memory
	writeBody := `{"id":1,"method":"memory_write","params":{"content":"hello from http","category":"knowledge","tags":"test"}}`
	req := httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(writeBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("write: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var writeResp Response
	if err := json.Unmarshal(w.Body.Bytes(), &writeResp); err != nil {
		t.Fatalf("parse write response: %v", err)
	}
	if writeResp.Error != "" {
		t.Fatalf("write error: %s", writeResp.Error)
	}

	// List memories
	listBody := `{"id":2,"method":"memory_list","params":{"category":"knowledge"}}`
	req = httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(listBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp Response
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("parse list response: %v", err)
	}
	if listResp.Error != "" {
		t.Fatalf("list error: %s", listResp.Error)
	}
}

func TestHTTPSearch(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	// Write first
	writeBody := `{"id":1,"method":"memory_write","params":{"content":"test memory for search"}}`
	req := httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(writeBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	// Search
	searchBody := `{"id":2,"method":"memory_search","params":{"query":"test memory"}}`
	req = httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(searchBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("search: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var searchResp Response
	if err := json.Unmarshal(w.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("parse search response: %v", err)
	}
	if searchResp.Error != "" {
		t.Fatalf("search error: %s", searchResp.Error)
	}
}

func TestHTTPToolsList(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	body := `{"id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	tools, ok := resp.Result.([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("expected non-empty tools list")
	}
}

func TestHTTPAgentCard(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var card map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("parse agent card: %v", err)
	}
	if card["name"] != "memory-cli" {
		t.Fatalf("expected name=memory-cli, got %v", card["name"])
	}
	skills, ok := card["skills"].([]any)
	if !ok || len(skills) == 0 {
		t.Fatal("expected non-empty skills")
	}
}

func TestHTTPInvalidJSON(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	req := httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader("not json")))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHTTPUnknownTool(t *testing.T) {
	srv := setupHTTPServer(t, []string{"test-key"})

	body := `{"id":1,"method":"nonexistent_tool","params":{}}`
	req := httptest.NewRequest("POST", "/a2a", io.NopCloser(strings.NewReader(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("expected error for unknown tool")
	}
}
