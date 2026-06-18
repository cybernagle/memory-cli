package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
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
	if v := q.Get("project"); v != "" {
		opts.Project = v
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

type heatmapDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
	Level int    `json:"level"`
}

func (srv *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	// Build heatmap from memory list (works without SQLite)
	all, err := srv.store.impl.List(store.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type dayCount struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
		Level int    `json:"level"`
	}

	countMap := make(map[string]int)
	// Hour-of-day (0-23) and weekday (0=Sunday..6=Saturday) distributions — reveal when the
	// user is most active (late-night hacking? weekday vs weekend?).
	hourCounts := make([]int, 24)
	weekdayCounts := make([]int, 7)
	for _, m := range all {
		key := m.CreatedAt.Format("2006-01-02")
		countMap[key]++
		hourCounts[m.CreatedAt.Hour()]++
		weekdayCounts[int(m.CreatedAt.Weekday())]++
	}

	var days []dayCount
	maxCount := 1
	total := 0
	for key, count := range countMap {
		if count > maxCount {
			maxCount = count
		}
		total += count
		days = append(days, dayCount{Date: key, Count: count})
	}

	// Sort by date
	sort.Slice(days, func(i, j int) bool {
		return days[i].Date < days[j].Date
	})

	maxStreak := 0
	currentStreak := 0
	for _, d := range days {
		if d.Count > 0 {
			currentStreak++
			if currentStreak > maxStreak {
				maxStreak = currentStreak
			}
		} else {
			currentStreak = 0
		}
	}

	for i := range days {
		days[i].Level = heatmapLevel(days[i].Count, maxCount)
	}

	avgDay := 0
	if len(days) > 0 {
		avgDay = total / len(days)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days": days,
		"summary": map[string]any{
			"total":   total,
			"days":    len(days),
			"streak":  maxStreak,
			"max_day": maxCount,
			"avg_day": avgDay,
		},
		"hours":    hourCounts,
		"weekdays": weekdayCounts,
	})
}

func heatmapLevel(count, max int) int {
	if count == 0 {
		return 0
	}
	ratio := float64(count) / float64(max)
	switch {
	case ratio > 0.75:
		return 4
	case ratio > 0.5:
		return 3
	case ratio > 0.25:
		return 2
	default:
		return 1
	}
}

func (srv *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "missing question")
		return
	}

	// Search for relevant memories
	results, err := srv.store.impl.Search(store.SearchOptions{
		Query: req.Question,
		Phase: store.PhaseOrganized,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}

	// Also try searching in processed
	processed, _ := srv.store.impl.Search(store.SearchOptions{
		Query: req.Question,
		Phase: store.PhaseProcessed,
	})
	results = append(results, processed...)

	// Limit context to top results by relevance (max 20)
	if len(results) > 20 {
		results = results[:20]
	}

	if len(results) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":   "No relevant memories found for your question.",
			"sources":  []any{},
			"question": req.Question,
		})
		return
	}

	// Build context from memories
	var sb strings.Builder
	for i, m := range results {
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n[%d] [%s] %s", i+1, m.Category, content))
		sb.WriteString("\n")
	}

	prompt := fmt.Sprintf(`You are a memory assistant. Answer the user's question based ONLY on the following memories. If the memories don't contain relevant information, say so. Cite memory IDs when referencing specific facts.

User's memories:
%s

Question: %s

Answer concisely in the same language as the question:`, sb.String(), req.Question)

	if srv.llm == nil {
		// Fallback: return raw search results
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":   "LLM not available. Here are the relevant memories:\n\n" + formatMemoryList(results[:min(10, len(results))]),
			"sources":  toSources(results[:min(10, len(results))]),
			"question": req.Question,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	resp, err := srv.llm.Chat(ctx, prompt)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":   "LLM error: " + err.Error(),
			"sources":  toSources(results[:min(5, len(results))]),
			"question": req.Question,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":   resp,
		"sources":  toSources(results[:min(10, len(results))]),
		"question": req.Question,
	})
}

func formatMemoryList(mems []*store.Memory) string {
	var lines []string
	for _, m := range mems {
		content := m.Content
		if len(content) > 150 {
			content = content[:150] + "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", m.Category, content))
	}
	return strings.Join(lines, "\n")
}

func toSources(mems []*store.Memory) []map[string]string {
	sources := make([]map[string]string, len(mems))
	for i, m := range mems {
		content := m.Content
		if len(content) > 150 {
			content = content[:150] + "..."
		}
		sources[i] = map[string]string{
			"id":       m.ID,
			"category": string(m.Category),
			"content":  content,
		}
	}
	return sources
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
