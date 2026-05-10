package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

// Store is the interface the dashboard needs. Matches *store.Store.
type Store interface {
	List(opts store.ListOptions) ([]*store.Memory, error)
	Search(opts store.SearchOptions) ([]*store.Memory, error)
	FindByID(id string) (*store.Memory, error)
	GetBacklinks(id string) ([]*store.Memory, error)
}

type storeWrapper struct {
	impl Store
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := ComputeStats(srv.store.impl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (srv *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := store.ListOptions{}

	if v := q.Get("phase"); v != "" {
		opts.Phase = store.Phase(v)
	}
	if v := q.Get("category"); v != "" {
		opts.Category = store.Category(v)
	}
	if v := q.Get("scope"); v != "" {
		opts.Scope = v
	}
	if v := q.Get("source"); v != "" {
		opts.Source = v
	}
	var limit int
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	applyRecent := q.Get("recent") == "true"
	if !applyRecent {
		opts.Limit = limit
	}

	memories, err := srv.store.impl.List(opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if applyRecent {
		dayAgo := time.Now().Add(-24 * time.Hour)
		var recent []*store.Memory
		for _, m := range memories {
			if m.CreatedAt.After(dayAgo) {
				recent = append(recent, m)
			}
		}
		memories = recent
	}

	if limit > 0 && len(memories) > limit {
		memories = memories[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"memories": memories,
		"total":    len(memories),
	})
}

func (srv *Server) handleMemoryDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mem, err := srv.store.impl.FindByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, mem)
}

func (srv *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mem, err := srv.store.impl.FindByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	backlinks, err := srv.store.impl.GetBacklinks(id)
	if err != nil {
		log.Printf("warning: get backlinks for %s: %v", id, err)
	}

	var forward []*store.Memory
	for _, linkID := range mem.Links {
		if m, err := srv.store.impl.FindByID(linkID); err == nil {
			forward = append(forward, m)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"forward":   forward,
		"backlinks": backlinks,
	})
}

func (srv *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'q'")
		return
	}

	opts := store.SearchOptions{Query: query}
	if v := q.Get("tags"); v != "" {
		opts.Tags = strings.Split(v, ",")
	}
	if v := q.Get("phase"); v != "" {
		opts.Phase = store.Phase(v)
	}

	results, err := srv.store.impl.Search(opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"count":   len(results),
	})
}

type graphNode struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Category  string `json:"category"`
	Phase     string `json:"phase"`
	LinkCount int    `json:"link_count"`
}

type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func (srv *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	all, err := srv.store.impl.List(store.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	nodes := make([]graphNode, 0, len(all))
	edges := make([]graphEdge, 0)
	nodeSet := make(map[string]bool)
	edgeSeen := make(map[string]bool)

	for _, mem := range all {
		label := mem.Content
		if len(label) > 50 {
			label = label[:50] + "..."
		}
		label = strings.SplitN(label, "\n", 2)[0]

		nodes = append(nodes, graphNode{
			ID:        mem.ID,
			Label:     label,
			Category:  string(mem.Category),
			Phase:     string(mem.Phase),
			LinkCount: len(mem.Links),
		})
		nodeSet[mem.ID] = true
	}

	for _, mem := range all {
		for _, linkID := range mem.Links {
			if !nodeSet[linkID] {
				continue
			}
			key := mem.ID + ":" + linkID
			rev := linkID + ":" + mem.ID
			if edgeSeen[key] || edgeSeen[rev] {
				continue
			}
			edges = append(edges, graphEdge{
				Source: mem.ID,
				Target: linkID,
			})
			edgeSeen[key] = true
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": nodes,
		"edges": edges,
		"stats": fmt.Sprintf("%d nodes, %d edges", len(nodes), len(edges)),
	})
}
