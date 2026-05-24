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
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/memories", s.handleMemories)
	s.mux.HandleFunc("/memories/", s.handleMemoryDetail)
	s.mux.HandleFunc("/recall", s.handleRecall)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/stats", s.handleStats)
	s.mux.HandleFunc("/stats/histogram", s.handleHistogram)
	s.mux.HandleFunc("/stats/aggregation", s.handleAggregation)
	s.mux.HandleFunc("/activity", s.handleActivity)
	s.mux.HandleFunc("/activity/heatmap", s.handleHeatmap)
	s.mux.HandleFunc("/process/status", s.handleProcessStatus)
	s.mux.HandleFunc("/process/events", s.handleProcessEvents)
	s.mux.HandleFunc("/plugins/components", s.handlePluginComponents)
	s.mux.HandleFunc("/plugins/processors", s.handlePluginProcessors)
	s.mux.HandleFunc("/plugins/ingests", s.handlePluginIngests)
	s.mux.HandleFunc("/plugins/entities", s.handlePluginEntities)
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
		Content  string   `json:"content"`
		Category string   `json:"category"`
		Scope    string   `json:"scope"`
		Tags     []string `json:"tags"`
		Source   string   `json:"source"`
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

	mem, err := s.store.Write(req.Content, store.PhaseInbox, cat, scope, req.Tags, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	logActivity(s, "write", mem.ID, "http", "")
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
	if search := q.Get("q"); search != "" {
		results, err := s.store.Search(store.SearchOptions{Query: search, Phase: opts.Phase, Scope: opts.Scope})
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
