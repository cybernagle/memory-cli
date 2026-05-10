package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"time"
)

//go:embed assets/*
var assetsFS embed.FS

type Server struct {
	store *storeWrapper
	mux   *http.ServeMux
}

func NewServer(s Store) *Server {
	srv := &Server{
		store: &storeWrapper{impl: s},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("GET /api/memories/{id}/links", srv.handleLinks)
	mux.HandleFunc("GET /api/memories/{id}", srv.handleMemoryDetail)
	mux.HandleFunc("GET /api/memories", srv.handleMemories)
	mux.HandleFunc("GET /api/search", srv.handleSearch)
	mux.HandleFunc("GET /api/graph", srv.handleGraph)
	mux.HandleFunc("GET /", srv.handleIndex)

	srv.mux = mux
	return srv
}

func (srv *Server) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:         addr,
		Handler:      srv.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
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
