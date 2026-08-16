package dashboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
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
	SearchLike(opts store.SearchOptions) ([]*store.Memory, error)
	SearchWithExpansion(opts store.SearchOptions) ([]*store.Memory, error)
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
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Offset = n
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
	if v := q.Get("project"); v != "" {
		opts.Project = v
	}
	if v := q.Get("session"); v != "" {
		opts.SessionID = v
	}
	if v := q.Get("category"); v != "" {
		opts.Category = store.Category(v)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			opts.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			end := t.Add(24*time.Hour - time.Second) // inclusive end-of-day
			opts.To = &end
		}
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

// handleTimeline returns memory counts grouped by day (and optionally by project), supporting
// time+space dimension queries. Accepts project, phase, from, to query params.
// This powers the "memories over time, per project" view that the growth chart alone can't do
// (it shows only a global cumulative line).
func (srv *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := store.ListOptions{}
	if v := q.Get("project"); v != "" {
		opts.Project = v
	}
	if v := q.Get("phase"); v != "" {
		opts.Phase = store.Phase(v)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			opts.CreatedAfter = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			end := t.Add(24*time.Hour - time.Second)
			opts.CreatedBefore = &end
		}
	}

	all, err := srv.store.impl.List(opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Group by day (date string) — and also track per-project counts for the space dimension.
	type dayBucket struct {
		Date     string         `json:"date"`
		Count    int            `json:"count"`
		Projects map[string]int `json:"projects,omitempty"`
	}
	buckets := make(map[string]*dayBucket)
	var dateOrder []string
	for _, m := range all {
		day := m.CreatedAt.Format("2006-01-02")
		b, ok := buckets[day]
		if !ok {
			b = &dayBucket{Date: day, Projects: make(map[string]int)}
			buckets[day] = b
			dateOrder = append(dateOrder, day)
		}
		b.Count++
		if m.Project != "" {
			b.Projects[m.Project]++
		}
	}

	// Sort by date ascending for a chronological timeline.
	sort.Slice(dateOrder, func(i, j int) bool { return dateOrder[i] < dateOrder[j] })
	days := make([]*dayBucket, 0, len(dateOrder))
	for _, d := range dateOrder {
		days = append(days, buckets[d])
	}

	// Top-level project totals across the whole range (space-dimension summary).
	projectTotals := make(map[string]int)
	for _, m := range all {
		if m.Project != "" {
			projectTotals[m.Project]++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days":      days,
		"total":     len(all),
		"projects":  projectTotals,
		"day_count": len(days),
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
	// Only load organized + processed memories for the graph — at 18k records, including
	// raw inbox conversation turns makes the force-directed layout unusable (O(n²) per frame)
	// and inbox fragments have no meaningful links among each other. Organized memories are
	// the consolidated knowledge nodes that actually form a useful relationship graph.
	all, err := srv.store.impl.List(store.ListOptions{Phase: store.PhaseOrganized})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Also include processed (extracted but not yet consolidated into organized).
	if processed, err := srv.store.impl.List(store.ListOptions{Phase: store.PhaseProcessed}); err == nil {
		all = append(all, processed...)
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
	type dayCount struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
		Level int    `json:"level"`
	}

	// SQL fast path: aggregate with GROUP BY instead of loading 18k rows into Go.
	// created_at is stored as RFC3339 TEXT; substr(created_at,1,10) extracts YYYY-MM-DD,
	// substr(created_at,12,2) extracts the hour, strftime('%w', created_at) the weekday.
	sqlStore, _ := srv.store.impl.(*store.SqliteStore)
	if sqlStore != nil {
		db := sqlStore.DB()

		// Per-day counts.
		dayRows, err := db.Query(`SELECT substr(created_at,1,10) AS d, COUNT(*) FROM memories GROUP BY d ORDER BY d`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var days []dayCount
		maxCount := 1
		total := 0
		for dayRows.Next() {
			var dc dayCount
			dayRows.Scan(&dc.Date, &dc.Count)
			if dc.Count > maxCount {
				maxCount = dc.Count
			}
			total += dc.Count
			days = append(days, dc)
		}
		dayRows.Close()

		// Streak (consecutive days with count > 0).
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

		// Hour-of-day and weekday distributions.
		hourCounts := make([]int, 24)
		hourRows, _ := db.Query(`SELECT CAST(substr(created_at,12,2) AS INTEGER), COUNT(*) FROM memories GROUP BY substr(created_at,12,2)`)
		if hourRows != nil {
			for hourRows.Next() {
				var h, c int
				hourRows.Scan(&h, &c)
				if h >= 0 && h < 24 {
					hourCounts[h] = c
				}
			}
			hourRows.Close()
		}

		weekdayCounts := make([]int, 7)
		wdRows, _ := db.Query(`SELECT CAST(strftime('%w', created_at) AS INTEGER), COUNT(*) FROM memories GROUP BY strftime('%w', created_at)`)
		if wdRows != nil {
			for wdRows.Next() {
				var wd, c int
				wdRows.Scan(&wd, &c)
				if wd >= 0 && wd < 7 {
					weekdayCounts[wd] = c
				}
			}
			wdRows.Close()
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
		return
	}

	// Fallback: iterate all (for non-SQLite stores).
	all, err := srv.store.impl.List(store.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	countMap := make(map[string]int)
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

// ─── Intent detection layer ───

func (srv *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string        `json:"question"`
		History  []chatMessage `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "missing question")
		return
	}

	// Run the ask workflow pipeline (context resolve → intent → search → LLM).
	answer, extra := runAskWorkflow(srv, r, req.Question, req.History)

	response := map[string]any{
		"answer":   answer,
		"sources":  []any{},
		"question": req.Question,
	}
	for k, v := range extra {
		response[k] = v
	}
	writeJSON(w, http.StatusOK, response)
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

func (srv *Server) handleEntityGraph(w http.ResponseWriter, r *http.Request) {
	sqlStore, ok := srv.store.impl.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": []any{}, "edges": []any{}})
		return
	}
	db := sqlStore.DB()
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	expandID := q.Get("expand")

	nodeIDs := make(map[string]bool)

	var nodes []eNode
	var edges []eEdge

	if expandID != "" {
		var expandName string
		db.QueryRow("SELECT name FROM entities WHERE id = ?", expandID).Scan(&expandName)
		rows, err := db.Query(`SELECT DISTINCT b.entity_id, e.name, e.kind, COUNT(*) as co
			FROM entity_mentions a JOIN entity_mentions b ON a.memory_id=b.memory_id AND a.entity_id!=b.entity_id
			JOIN entities e ON e.id=b.entity_id WHERE a.entity_id=?
			GROUP BY b.entity_id,e.name,e.kind ORDER BY co DESC LIMIT ?`, expandID, limit)
		if err == nil {
			for rows.Next() {
				var n eNode
				rows.Scan(&n.ID, &n.Label, &n.Kind, &n.Mentions)
				nodes, nodeIDs[n.ID] = append(nodes, n), true
			}
			rows.Close()
		}
		if expandName != "" {
			var mc int
			db.QueryRow("SELECT COUNT(*) FROM entity_mentions WHERE entity_id=?", expandID).Scan(&mc)
			nodes = append(nodes, eNode{ID: expandID, Label: expandName, Mentions: mc})
			nodeIDs[expandID] = true
		}
	} else {
		// Two modes:
		// - No q: top N entities overall by mention count.
		// - With q: find entities matching the query (seed), then load their 1-hop co-occurrence
		//   neighbors so the graph shows the query entity IN ITS CONTEXT, not isolated.
		searchQ := q.Get("q")
		var rows *sql.Rows
		var err error
		if searchQ != "" {
			// Step 1: find seed entities matching the query.
			seedIDs := make(map[string]bool)
			rows, err = db.Query(`SELECT e.id,e.name,e.kind,COUNT(em.id) as mc
				FROM entities e JOIN entity_mentions em ON em.entity_id=e.id
				WHERE e.name LIKE ? AND LENGTH(e.name) <= 40 AND e.name NOT LIKE '%[[%'
				GROUP BY e.id,e.name,e.kind ORDER BY mc DESC`, "%"+searchQ+"%")
			if err == nil {
				for rows.Next() {
					var n eNode
					rows.Scan(&n.ID, &n.Label, &n.Kind, &n.Mentions)
					nodes, nodeIDs[n.ID] = append(nodes, n), true
					seedIDs[n.ID] = true
				}
				rows.Close()
			}
			// Step 2: load co-occurrence neighbors of seed entities (top N by co-occurrence).
			// These are entities that appear in the same memories as the seed — they provide
			// the context the user wants to see.
			if len(seedIDs) > 0 && len(nodes) < limit {
				// Build seed ID list for SQL IN clause.
				idArgs := make([]interface{}, 0, len(seedIDs))
				placeholders := ""
				i := 0
				for id := range seedIDs {
					if i > 0 {
						placeholders += ","
					}
					placeholders += "?"
					idArgs = append(idArgs, id)
					i++
				}
				remaining := limit - len(nodes)
				// Args: seed IDs (for IN), seed IDs again (for NOT IN), remaining limit.
				neighborArgs := make([]interface{}, 0)
				for id := range seedIDs { neighborArgs = append(neighborArgs, id) }
				neighborArgs = append(neighborArgs, remaining) // LIMIT
				neighborRows, _ := db.Query(`SELECT b.entity_id, e.name, e.kind, COUNT(*) as co
					FROM entity_mentions a
					JOIN entity_mentions b ON a.memory_id = b.memory_id AND a.entity_id != b.entity_id
					JOIN entities e ON e.id = b.entity_id
					WHERE a.entity_id IN (`+placeholders+`)
					AND LENGTH(e.name) <= 40 AND e.name NOT LIKE '%[[%'
					GROUP BY b.entity_id, e.name, e.kind
					ORDER BY co DESC LIMIT ?`, neighborArgs...)
				if neighborRows != nil {
					for neighborRows.Next() {
						var n eNode
						neighborRows.Scan(&n.ID, &n.Label, &n.Kind, &n.Mentions)
						if !nodeIDs[n.ID] {
							nodes, nodeIDs[n.ID] = append(nodes, n), true
						}
					}
					neighborRows.Close()
				}
			}
			// If entity table had few/no results OR no edges, use content-based graph.
			// Entity table only covers [[wiki-links]] — most terms like "contract" never get
			// wiki-linked, and even "juli" returns 0 co-occurrence edges because entity_mentions
			// co-occurrence is sparse. Content-based extracts terms from actual memory text.
			if searchQ != "" && (len(nodes) == 0 || len(edges) < 2) {
				contentNodes, contentEdges := buildContentGraph(db, searchQ, limit)
				if len(contentNodes) > 0 && len(contentEdges) > len(edges) {
					nodes, edges = contentNodes, contentEdges
					writeJSON(w, http.StatusOK, map[string]any{
						"nodes": nodes, "edges": edges,
						"stats": fmt.Sprintf("%d nodes, %d edges (content-based)", len(nodes), len(edges)),
					})
					return
				}
			}
		} else {
			rows, err = db.Query(`SELECT e.id,e.name,e.kind,COUNT(em.id) as mc
				FROM entities e JOIN entity_mentions em ON em.entity_id=e.id
				WHERE LENGTH(e.name) <= 40 AND e.name NOT LIKE '%[[%'
				GROUP BY e.id,e.name,e.kind ORDER BY mc DESC LIMIT ?`, limit)
			if err == nil {
				for rows.Next() {
					var n eNode
					rows.Scan(&n.ID, &n.Label, &n.Kind, &n.Mentions)
					nodes, nodeIDs[n.ID] = append(nodes, n), true
				}
				rows.Close()
			}
		}
		if err == nil {
			for rows.Next() {
				var n eNode
				rows.Scan(&n.ID, &n.Label, &n.Kind, &n.Mentions)
				nodes, nodeIDs[n.ID] = append(nodes, n), true
			}
			rows.Close()
		}
	}

	// Co-occurrence edges between loaded nodes.
	coRows, _ := db.Query(`SELECT a.entity_id,b.entity_id,COUNT(*) as w
		FROM entity_mentions a JOIN entity_mentions b ON a.memory_id=b.memory_id AND a.entity_id<b.entity_id
		GROUP BY a.entity_id,b.entity_id HAVING w>=2`)
	if coRows != nil {
		for coRows.Next() {
			var from, to string
			var w int
			coRows.Scan(&from, &to, &w)
			if nodeIDs[from] && nodeIDs[to] {
				edges = append(edges, eEdge{From: from, To: to, Weight: w})
			}
		}
		coRows.Close()
	}

	if nodes == nil {
		nodes = []eNode{}
	}
	if edges == nil {
		edges = []eEdge{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": nodes, "edges": edges,
		"stats": fmt.Sprintf("%d nodes, %d edges", len(nodes), len(edges)),
	})
}

// buildContentGraph builds an entity graph from memory CONTENT when the entity table
// has no match for the query (e.g. "contract" was never a [[wiki-link]]). It searches
// memories containing the query term, extracts frequent co-occurring terms, and constructs
// nodes + co-occurrence edges dynamically.
type eNode struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Mentions int    `json:"mentions"`
	Kind     string `json:"kind"`
}
type eEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight int    `json:"weight"`
}

func buildContentGraph(db *sql.DB, query string, limit int) ([]eNode, []eEdge) {
	// Step 1: find memories containing the query term.
	rows, err := db.Query(`SELECT id, content FROM memories
		WHERE content LIKE ? AND phase IN ('organized','processed')
		ORDER BY created_at DESC LIMIT 200`, "%"+query+"%")
	if err != nil {
		return nil, nil
	}

	type memData struct {
		id      string
		content string
	}
	var mems []memData
	for rows.Next() {
		var m memData
		rows.Scan(&m.id, &m.content)
		mems = append(mems, m)
	}
	rows.Close()

	if len(mems) == 0 {
		return nil, nil
	}

	// Step 2: extract terms from each memory, count per-memory frequency, and build co-occurrence.
	termFreq := make(map[string]int)       // term → number of memories containing it
	coOccur := make(map[string]int)        // "termA\x00termB" → co-occurrence count
	var termList []string                   // unique terms ordered by frequency
	termSet := make(map[string]bool)

	// Also include the query term itself as a node.
	queryLower := strings.ToLower(query)
	termFreq[queryLower] = len(mems)
	termSet[queryLower] = true
	termList = append(termList, queryLower)

	for _, m := range mems {
		content := strings.ToLower(m.content)
		// Extract CJK 2-3 char segments + ASCII words from this memory.
		var memTerms []string
		seen := make(map[string]bool)
		for _, seg := range cjkGraphRe.FindAllString(content, -1) {
			runes := []rune(seg)
			for n := 2; n <= 3 && n <= len(runes); n++ {
				t := string(runes[:n])
				if !graphStopWord(t) && !seen[t] {
					seen[t] = true
					memTerms = append(memTerms, t)
				}
			}
		}
		for _, w := range asciiGraphRe.FindAllString(content, -1) {
			if len(w) >= 3 && !graphStopWord(w) && !seen[w] {
				seen[w] = true
				memTerms = append(memTerms, w)
			}
		}

		// Add query term to this memory's terms.
		if !seen[queryLower] {
			memTerms = append(memTerms, queryLower)
		}

		// Update frequency and co-occurrence.
		for _, t := range memTerms {
			termFreq[t]++
			if !termSet[t] {
				termSet[t] = true
				termList = append(termList, t)
			}
		}
		for i := 0; i < len(memTerms); i++ {
			for j := i + 1; j < len(memTerms); j++ {
				a, b := memTerms[i], memTerms[j]
				if a > b { a, b = b, a }
				coOccur[a+"\x00"+b]++
			}
		}
	}

	// Step 3: pick top N terms by frequency (min 3 = appears in 3+ memories).
	threshold := 3
	if len(mems) > 50 {
		threshold = len(mems) / 20 // 5% threshold for large sets
	}

	type termEntry struct {
		term string
		freq int
	}
	var candidates []termEntry
	for _, t := range termList {
		if termFreq[t] >= threshold {
			candidates = append(candidates, termEntry{t, termFreq[t]})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].freq > candidates[j].freq })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Build nodes.
	selectedSet := make(map[string]bool)
	var nodes []eNode
	for _, c := range candidates {
		nodes = append(nodes, eNode{ID: c.term, Label: c.term, Mentions: c.freq})
		selectedSet[c.term] = true
	}

	// Build edges: co-occurrence between selected terms, weight >= 2.
	var edges []eEdge
	for key, weight := range coOccur {
		if weight < 2 {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		if selectedSet[parts[0]] && selectedSet[parts[1]] {
			edges = append(edges, eEdge{From: parts[0], To: parts[1], Weight: weight})
		}
	}

	if nodes == nil {
		nodes = []eNode{}
	}
	if edges == nil {
		edges = []eEdge{}
	}
	return nodes, edges
}

var (
	cjkGraphRe   = regexp.MustCompile(`[\x{4e00}-\x{9fff}]+`)
	asciiGraphRe = regexp.MustCompile(`[a-z0-9][a-z0-9._-]+`)
)

func graphStopWord(w string) bool {
	// Pure numbers are noise.
	allDigit := true
	for _, r := range w {
		if r < '0' || r > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return true
	}
	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "from": true,
		"this": true, "that": true, "are": true, "was": true, "were": true,
		"has": true, "have": true, "not": true, "but": true, "all": true,
		"can": true, "will": true, "http": true, "https": true, "www": true,
		"com": true, "org": true, "true": true, "false": true, "null": true,
	}
	return stopWords[w]
}

func (srv *Server) handleEntityMemories(w http.ResponseWriter, r *http.Request) {
	entityID := r.URL.Query().Get("entity_id")
	if entityID == "" {
		writeError(w, http.StatusBadRequest, "entity_id required")
		return
	}
	sqlStore, ok := srv.store.impl.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"memories": []any{}})
		return
	}
	db := sqlStore.DB()

	rows, err := db.Query("SELECT memory_id FROM entity_mentions WHERE entity_id=?", entityID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var memIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		memIDs = append(memIDs, id)
	}
	rows.Close()

	var entityName string
	db.QueryRow("SELECT name FROM entities WHERE id=?", entityID).Scan(&entityName)

	if len(memIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"memories": []any{}, "entity": entityName, "count": 0})
		return
	}

	// Load memories and filter to entity's set.
	allMems, _ := srv.store.impl.List(store.ListOptions{Limit: 500})
	idSet := make(map[string]bool)
	for _, id := range memIDs {
		idSet[id] = true
	}
	var filtered []*store.Memory
	for _, m := range allMems {
		if idSet[m.ID] {
			if len(m.Content) > 150 {
				m.Content = m.Content[:150] + "..."
			}
			filtered = append(filtered, m)
			if len(filtered) >= 20 {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memories": filtered, "entity": entityName,
		"count": len(filtered), "total_mentions": len(memIDs),
	})
}

// handleEventStats serves metrics over the append-only event log (raw_entries) — the
// source-of-truth counterpart to /api/stats (which describes derived data).
func (srv *Server) handleEventStats(w http.ResponseWriter, r *http.Request) {
	stats, err := ComputeEventStats(srv.store.impl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleSessions serves the per-session projection (session_views): task/entity/facet/
// summary/lessons per session — the "what did I do" read model.
func (srv *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SessionViewFilter{Limit: 50}
	if v := q.Get("session"); v != "" {
		f.SessionID = v
	}
	if v := q.Get("project"); v != "" {
		f.Project = v
	}
	if v := q.Get("entity"); v != "" {
		f.Entity = v
	}
	views, err := srv.sqliteSessionViews(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views == nil {
		views = []*store.SessionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(views), "sessions": views})
}

// sqliteSessionViews resolves session views when the backing store is SQLite (the
// session_views projection is SQLite-only; FileStore returns no sessions).
func (srv *Server) sqliteSessionViews(f store.SessionViewFilter) ([]*store.SessionView, error) {
	if sqlStore, ok := srv.store.impl.(*store.SqliteStore); ok {
		return sqlStore.ListSessionViews(f)
	}
	return nil, nil
}
