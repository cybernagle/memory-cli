package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/store"
)

//go:embed dashboard.html
var dashboardFS embed.FS

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := dashboardFS.ReadFile("dashboard.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type Server struct {
	store    store.Store
	mux      *http.ServeMux
	keys     []string
	start    time.Time
	dbPath   string
	registry *plugin.Registry
}

func NewServer(s store.Store, dbPath string, keys []string) *Server {
	srv := &Server{
		store:  s,
		mux:    http.NewServeMux(),
		keys:   keys,
		start:  time.Now(),
		dbPath: dbPath,
	}
	srv.routes()
	return srv
}

func (s *Server) SetRegistry(r *plugin.Registry) {
	s.registry = r
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// All handlers except /health are wrapped with auth middleware. When no API keys are
	// configured (len(s.keys)==0), auth is a pass-through (no-op). When keys ARE set, every
	// request must carry Authorization: Bearer <key>. /health is exempt so probes work.
	s.mux.HandleFunc("/", s.auth(s.handleIndex))
	s.mux.HandleFunc("/memories", s.auth(s.handleMemories))
	s.mux.HandleFunc("/memories/", s.auth(s.handleMemoryDetail))
	s.mux.HandleFunc("/recall", s.auth(s.handleRecall))
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/stats", s.auth(s.handleStats))
	s.mux.HandleFunc("/stats/histogram", s.auth(s.handleHistogram))
	s.mux.HandleFunc("/stats/aggregation", s.auth(s.handleAggregation))
	s.mux.HandleFunc("/activity", s.auth(s.handleActivity))
	s.mux.HandleFunc("/activity/heatmap", s.auth(s.handleHeatmap))
	s.mux.HandleFunc("/process/status", s.auth(s.handleProcessStatus))
	s.mux.HandleFunc("/process/events", s.auth(s.handleProcessEvents))
	s.mux.HandleFunc("/plugins/components", s.auth(s.handlePluginComponents))
	s.mux.HandleFunc("/plugins/processors", s.auth(s.handlePluginProcessors))
	s.mux.HandleFunc("/plugins/ingests", s.auth(s.handlePluginIngests))
	s.mux.HandleFunc("/plugins/entities", s.auth(s.handlePluginEntities))
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if len(s.keys) == 0 {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		for _, key := range s.keys {
			if token == key {
				next(w, r)
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listMemories(w, r)
	case http.MethodPost:
		s.createMemory(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content  string         `json:"content"`
		Category string         `json:"category"`
		Scope    string         `json:"scope"`
		Tags     []string       `json:"tags"`
		Source   string         `json:"source"`
		Project  string         `json:"project"`
		Role     string         `json:"role"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	cat := store.Category(req.Category)
	if cat == "" {
		cat = store.CategoryInbox
	}
	scope := req.Scope
	if scope == "" {
		scope = "global"
	}
	source := req.Source
	if source == "" {
		source = "http"
	}

	// Full-field write: if any extended field (project/role/metadata) is set, use IngestMemory
	// which persists all Memory struct fields. Otherwise fall back to the simple Write path.
	if req.Project != "" || req.Role != "" || len(req.Metadata) > 0 {
		mem := &store.Memory{
			Content:   req.Content,
			Phase:     store.PhaseInbox,
			Category:  cat,
			Scope:     scope,
			Tags:      req.Tags,
			Source:    source,
			Project:   req.Project,
			Role:      req.Role,
			Metadata:  req.Metadata,
		}
		if sqlStore, ok := s.store.(*store.SqliteStore); ok {
			if err := sqlStore.IngestMemory(mem); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			logActivity(s, "write", mem.ID, source, req.Project)
			writeJSON(w, http.StatusCreated, mem)
			return
		}
		// Non-SQLite store: fall back to Write (loses project/role/metadata, logged as warning)
	}

	mem, err := s.store.Write(req.Content, store.PhaseInbox, cat, scope, req.Tags, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	logActivity(s, "write", mem.ID, source, "")
	writeJSON(w, http.StatusCreated, mem)
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	opts := store.ListOptions{}

	q := r.URL.Query()
	if phase := q.Get("phase"); phase != "" {
		opts.Phase = store.Phase(phase)
	}
	if cat := q.Get("category"); cat != "" {
		opts.Category = store.Category(cat)
	}
	if scope := q.Get("scope"); scope != "" {
		opts.Scope = scope
	}
	if source := q.Get("source"); source != "" {
		opts.Source = source
	}
	if tags := q.Get("tags"); tags != "" {
		// Comma-separated = AND filter (memory must have ALL listed tags).
		opts.Tags = strings.Split(tags, ",")
	}
	if search := q.Get("q"); search != "" {
		results, err := s.store.Search(store.SearchOptions{Query: search, Phase: opts.Phase, Scope: opts.Scope, Tags: opts.Tags})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"memories": results})
		return
	}
	if limit := q.Get("limit"); limit != "" {
		fmt.Sscanf(limit, "%d", &opts.Limit)
	}
	if recent := q.Get("recent"); recent == "true" {
		t := time.Now().Add(-24 * time.Hour)
		opts.CreatedAfter = &t
	}
	// from=/to= support absolute (RFC3339 or YYYY-MM-DD) and relative (-7d, -30d, -12h).
	if from := q.Get("from"); from != "" {
		if t, ok := parseTimeParam(from); ok {
			opts.CreatedAfter = &t
		}
	}
	if to := q.Get("to"); to != "" {
		if t, ok := parseTimeParam(to); ok {
			opts.CreatedBefore = &t
		}
	}

	memories, err := s.store.List(opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": memories})
}

func (s *Server) handleMemoryDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/memories/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		mem, err := s.store.FindByID(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, mem)
	case http.MethodDelete:
		if err := s.store.Delete(id); err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		logActivity(s, "delete", id, "http", "")
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		// Partial metadata update — the proposal status-machine path. Merges the patch into
		// existing metadata (non-destructive). makro calls this on accept/reject/ignore.
		var patch struct {
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if len(patch.Metadata) == 0 {
			writeError(w, http.StatusBadRequest, "metadata patch required")
			return
		}
		if sqlStore, ok := s.store.(*store.SqliteStore); ok {
			if err := sqlStore.UpdateMemoryMetadata(id, patch.Metadata); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			logActivity(s, "patch", id, "http", "metadata")
			// Return the updated memory.
			mem, _ := s.store.FindByID(id)
			writeJSON(w, http.StatusOK, mem)
		} else {
			writeError(w, http.StatusNotImplemented, "metadata update requires sqlite backend")
		}
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status := map[string]any{
		"status":  "ok",
		"uptime":  time.Since(s.start).String(),
		"db_path": s.dbPath,
		"version": "0.2.0",
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	stats, err := computeStats(s.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func computeStats(s store.Store) (map[string]any, error) {
	all, err := s.List(store.ListOptions{})
	if err != nil {
		return nil, err
	}

	inbox := 0
	organized := 0
	processed := 0
	cats := map[string]int{}
	tags := map[string]int{}
	for _, m := range all {
		switch m.Phase {
		case store.PhaseInbox:
			inbox++
		case store.PhaseOrganized:
			organized++
		default:
			processed++
		}
		cats[string(m.Category)]++
		for _, t := range m.Tags {
			tags[t]++
		}
	}

	return map[string]any{
		"total":      len(all),
		"inbox":      inbox,
		"organized":  organized,
		"processed":  processed,
		"categories": cats,
		"tags":       tags,
	}, nil
}

func logActivity(s *Server, action, memoryID, source, detail string) {
	if ss, ok := s.store.(*store.SqliteStore); ok {
		ss.LogActivity(action, memoryID, source, detail)
	}
}

// parseTimeParam parses a time query param. Supports relative ("-7d", "-12h", "-30m") and
// absolute ("2026-06-20" or RFC3339). Returns ok=false if unparseable.
func parseTimeParam(s string) (time.Time, bool) {
	// Relative: -<n><unit> where unit is d/h/m
	if strings.HasPrefix(s, "-") {
		var n int
		var unit string
		if _, err := fmt.Sscanf(s, "-%d%s", &n, &unit); err == nil && n > 0 {
			switch unit {
			case "d", "D":
				t := time.Now().AddDate(0, 0, -n)
				return t, true
			case "h", "H":
				t := time.Now().Add(-time.Duration(n) * time.Hour)
				return t, true
			case "m", "M":
				t := time.Now().Add(-time.Duration(n) * time.Minute)
				return t, true
			}
		}
	}
	// Absolute date: YYYY-MM-DD
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	// Absolute RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
