package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
)

//go:embed assets/*
var assetsFS embed.FS

type Server struct {
	store *storeWrapper
	llm   *llm.Client
	mux   *http.ServeMux
}

func NewServer(s Store, llmClient *llm.Client) *Server {
	srv := &Server{
		store: &storeWrapper{impl: s},
		llm:   llmClient,
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("GET /api/event-stats", srv.handleEventStats)
	mux.HandleFunc("GET /api/heatmap", srv.handleHeatmap)
	mux.HandleFunc("GET /api/memories/{id}/links", srv.handleLinks)
	mux.HandleFunc("GET /api/memories/{id}", srv.handleMemoryDetail)
	mux.HandleFunc("GET /api/memories", srv.handleMemories)
	mux.HandleFunc("GET /api/memories/timeline", srv.handleTimeline)
	mux.HandleFunc("GET /api/search", srv.handleSearch)
	mux.HandleFunc("GET /api/graph", srv.handleGraph)
	mux.HandleFunc("GET /api/entity-graph", srv.handleEntityGraph)
	mux.HandleFunc("GET /api/entity-graph/memories", srv.handleEntityMemories)
	mux.HandleFunc("POST /api/ask", srv.handleAsk)
	mux.HandleFunc("POST /api/ask/agent", srv.handleAskAgent)
	mux.HandleFunc("GET /api/architecture.svg", srv.handleArchitectureSVG)
	mux.HandleFunc("GET /", srv.handleIndex)

	srv.mux = mux
	return srv
}

func (srv *Server) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:         addr,
		Handler:      srv.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return server.ListenAndServe()
}

func (srv *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(assetsFS, "assets/index.html")
	if err != nil {
		http.Error(w, "failed to load dashboard", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleArchitectureSVG serves the embedded architecture diagram for the dashboard's 架构 tab.
// It lives in assets/ (go:embed) so the binary is self-contained.
func (srv *Server) handleArchitectureSVG(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(assetsFS, "assets/architecture.svg")
	if err != nil {
		http.Error(w, "architecture diagram not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(data)
}
