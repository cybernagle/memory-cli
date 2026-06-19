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

func (srv *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "missing question")
		return
	}

	// Extract search keywords from the question. FTS5 with unicode61 tokenizer treats a full
	// Chinese sentence as one giant token → 0 matches. We split into individual tokens
	// (English words stay intact; Chinese gets broken into 2-char bigrams) so FTS can match.
	searchQuery := extractSearchKeywords(req.Question)

	// Search for relevant memories across organized + processed phases.
	results, err := srv.store.impl.Search(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseOrganized,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}

	// Also try searching in processed
	processed, _ := srv.store.impl.Search(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseProcessed,
	})

	// Also search inbox — it holds the raw conversation turns with the ORIGINAL timestamps.
	// organized memories carry the processing date, not the event date, so time questions
	// ("when did I do X") can only be answered from inbox.
	inbox, _ := srv.store.impl.Search(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseInbox,
	})

	// Build context with balanced phase representation. If we just concatenated and truncated
	// to 20, organized (215 results for a hot topic) would crowd out inbox (the only source
	// with real dates). Allocate slots: organized gets 8 (dense summaries), inbox gets 8
	// (raw turns with timestamps), processed gets 4. Sort each by date desc within its slot.
	const orgLimit, inboxLimit, procLimit = 8, 8, 4
	if len(results) > orgLimit {
		results = results[:orgLimit]
	}
	if len(processed) > procLimit {
		processed = processed[:procLimit]
	}
	// For inbox, pick items spread across different dates (not all from one day) so the LLM
	// sees the full time span. Sort ASCENDING (oldest first) — for "when did I do X" questions,
	// the earliest date (project start) is the most important to surface.
	sort.Slice(inbox, func(i, j int) bool {
		return inbox[i].CreatedAt.Before(inbox[j].CreatedAt)
	})
	inbox = spreadByDate(inbox, inboxLimit)
	results = append(results, processed...)
	results = append(results, inbox...)

	if len(results) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":   "No relevant memories found for your question.",
			"sources":  []any{},
			"question": req.Question,
		})
		return
	}

	// Build context from memories — INCLUDE the date so the LLM can answer "when" questions.
	// Without the date, the model sees only content and cannot tell when something happened.
	var sb strings.Builder
	for i, m := range results {
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		dateStr := m.CreatedAt.Format("2006-01-02 15:04")
		phase := string(m.Phase)
		sb.WriteString(fmt.Sprintf("\n[%d] [%s] [%s] [%s] %s", i+1, dateStr, phase, m.Category, content))
		sb.WriteString("\n")
	}

	prompt := fmt.Sprintf(`You are a memory assistant. Answer the user's question based ONLY on the following memories.

IMPORTANT about dates: each memory has a [YYYY-MM-DD HH:MM] timestamp. For [inbox] memories, this is the DATE THE EVENT HAPPENED (when the user actually did/said something). For [organized] memories, this is the processing date, NOT the event date. When asked "when" something happened, PREFER inbox dates over organized dates.

Cite memory numbers [N] when referencing facts.

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

// spreadByDate picks up to n items from memories, spread across distinct dates so the LLM
// sees the full time span (not 8 items all from the same day). Groups by date, then round-
// robins one from each date until the limit is reached.
func spreadByDate(mems []*store.Memory, n int) []*store.Memory {
	if len(mems) <= n {
		return mems
	}
	// Group by date.
	byDate := map[string][]*store.Memory{}
	var dateOrder []string
	for _, m := range mems {
		d := m.CreatedAt.Format("2006-01-02")
		if _, ok := byDate[d]; !ok {
			dateOrder = append(dateOrder, d)
		}
		byDate[d] = append(byDate[d], m)
	}
	// Round-robin across dates.
	var out []*store.Memory
	for round := 0; len(out) < n; round++ {
		added := false
		for _, d := range dateOrder {
			if round < len(byDate[d]) {
				out = append(out, byDate[d][round])
				added = true
				if len(out) >= n {
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return out
}
// FTS5's unicode61 tokenizer treats each maximal run of CJK characters as a single token
// (no per-character segmentation), so a full Chinese sentence → 0 matches. Strategy:
//   - Extract ASCII words (RSA, GLM-4.5, 2026) — these are high-precision FTS tokens.
//   - Extract meaningful CJK words: split on punctuation/spaces, filter out stop words
//     (什么/怎么/的/是/吗/呢/大概/时间), keep content words ≥2 chars.
//   - Join with spaces → FTS implicit AND across tokens.
//
// If we end up with nothing usable, fall back to the raw question (let LIKE handle it).
func extractSearchKeywords(question string) string {
	var tokens []string

	// Extract ASCII words (letters/digits/hyphens/dots, length ≥ 2).
	var ascii strings.Builder
	flushASCII := func() {
		if ascii.Len() >= 2 {
			tokens = append(tokens, ascii.String())
		}
		ascii.Reset()
	}

	// CJK stop words — question fillers that won't help find content.
	stopWords := map[string]bool{
		"什么": true, "怎么": true, "的吗": true,
		"的": true, "是": true, "吗": true, "呢": true, "了": true,
		"大概": true, "时间": true, "时候": true, "哪些": true,
		"这个": true, "那个": true, "可以": true, "应该": true,
		"关于": true, "对于": true, "我想": true, "帮我": true,
		"请问": true, "一下": true, "有没有": true, "知道": true,
		"制作": true, "做的": true, "时候的": true, "吗的": true,
		"在哪": true, "如何": true, "为何": true,
	}

	// Split CJK segments on non-CJK, then filter stop words.
	var cjkSeg strings.Builder
	flushCJK := func() {
		seg := strings.TrimSpace(cjkSeg.String())
		cjkSeg.Reset()
		if seg == "" {
			return
		}
		// Try to find meaningful sub-words. Since unicode61 indexes the whole CJK run,
		// any substring of it is matchable. Extract likely content words by removing
		// known stop-word prefixes/suffixes.
		for _, sw := range []string{"的吗", "的吗", "的吗", "什么", "怎么", "大概", "时间", "时候", "这个", "那个", "制作", "做的"} {
			seg = strings.ReplaceAll(seg, sw, "")
		}
		seg = strings.TrimSpace(seg)
		if len([]rune(seg)) >= 2 && !stopWords[seg] {
			tokens = append(tokens, seg)
		}
	}

	for _, r := range question {
		if r >= '\u4e00' && r <= '\u9fff' {
			flushASCII()
			cjkSeg.WriteRune(r)
		} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			flushCJK()
			ascii.WriteRune(r)
		} else {
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()

	// Dedup.
	seen := make(map[string]bool)
	var out []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return question
	}
	// Use FTS OR (not default AND): a memory matching ANY keyword is relevant. AND is too
	// strict for natural-language questions — "RSA 加密算法视频" as AND returns 0 because no
	// single memory contains both as exact FTS tokens. OR returns the union, which the LLM
	// can then rank/filter by relevance.
	return strings.Join(out, " OR ")
}
