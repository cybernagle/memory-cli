package dashboard

import (
	"context"
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

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// Store is the interface the dashboard needs. Matches *store.Store.
type Store interface {
	List(opts store.ListOptions) ([]*store.Memory, error)
	Search(opts store.SearchOptions) ([]*store.Memory, error)
	FindByID(id string) (*store.Memory, error)
	GetBacklinks(id string) ([]*store.Memory, error)
	SearchLike(opts store.SearchOptions) ([]*store.Memory, error)
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
// detectTimeIntent checks for time-based questions and returns a date range.
// Supports: 今天/昨天/前天, X月X号, 上周/这周, 近N天, X月X号到Y月Y号, YYYY-MM-DD.
type dateRange struct {
	from, to time.Time
	label    string
}

func detectTimeIntent(question string) (*dateRange, bool) {
	now := time.Now()
	loc := now.Location()
	lower := strings.ToLower(question)

	if m := regexp.MustCompile(`(?:近|最近)\s*(\d+)\s*天`).FindStringSubmatch(question); m != nil {
		n, _ := strconv.Atoi(m[1])
		return &dateRange{from: now.AddDate(0, 0, -n), to: now, label: fmt.Sprintf("近%d天", n)}, true
	}
	if strings.Contains(question, "上周") || strings.Contains(lower, "last week") {
		thisMonday := now.AddDate(0, 0, -(int(now.Weekday())+6)%7)
		lastMonday := thisMonday.AddDate(0, 0, -7)
		lastSunday := lastMonday.AddDate(0, 0, 6)
		return &dateRange{from: lastMonday, to: lastSunday, label: fmt.Sprintf("上周(%s到%s)", lastMonday.Format("1月2日"), lastSunday.Format("1月2日"))}, true
	}
	if strings.Contains(question, "这周") || strings.Contains(question, "本周") || strings.Contains(lower, "this week") {
		thisMonday := now.AddDate(0, 0, -(int(now.Weekday())+6)%7)
		return &dateRange{from: thisMonday, to: now, label: fmt.Sprintf("这周(%s至今)", thisMonday.Format("1月2日"))}, true
	}
	if strings.Contains(question, "今天") || strings.Contains(lower, "today") {
		s := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return &dateRange{from: s, to: s.Add(24*time.Hour - time.Second), label: s.Format("2006-01-02")}, true
	}
	if strings.Contains(question, "昨天") || strings.Contains(lower, "yesterday") {
		d := now.AddDate(0, 0, -1)
		s := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		return &dateRange{from: s, to: s.Add(24*time.Hour - time.Second), label: s.Format("2006-01-02")}, true
	}
	if strings.Contains(question, "前天") {
		d := now.AddDate(0, 0, -2)
		s := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		return &dateRange{from: s, to: s.Add(24*time.Hour - time.Second), label: s.Format("2006-01-02")}, true
	}
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]\s*(?:到|至|~|-|—)\s*(\d{1,2})月(\d{1,2})[号日]`).FindStringSubmatch(question); m != nil {
		m1, _ := strconv.Atoi(m[1]); d1, _ := strconv.Atoi(m[2])
		m2, _ := strconv.Atoi(m[3]); d2, _ := strconv.Atoi(m[4])
		from := time.Date(now.Year(), time.Month(m1), d1, 0, 0, 0, 0, loc)
		to := time.Date(now.Year(), time.Month(m2), d2, 23, 59, 59, 0, loc)
		if from.After(now) { from = from.AddDate(-1, 0, 0); to = to.AddDate(-1, 0, 0) }
		return &dateRange{from: from, to: to, label: fmt.Sprintf("%s到%s", from.Format("1月2日"), to.Format("1月2日"))}, true
	}
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]`).FindStringSubmatch(question); m != nil {
		month, _ := strconv.Atoi(m[1]); day, _ := strconv.Atoi(m[2])
		t := time.Date(now.Year(), time.Month(month), day, 0, 0, 0, 0, loc)
		if t.After(now.AddDate(0, 0, 1)) { t = t.AddDate(-1, 0, 0) }
		return &dateRange{from: t, to: t.Add(24*time.Hour - time.Second), label: t.Format("2006-01-02")}, true
	}
	if m := regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`).FindStringSubmatch(question); m != nil {
		if t, err := time.Parse("2006-01-02", m[0]); err == nil {
			return &dateRange{from: t, to: t.Add(24*time.Hour - time.Second), label: t.Format("2006-01-02")}, true
		}
	}
	return nil, false
}

// detectAggregateIntent: "有多少" / "有哪些" / "哪些" / "列出" / "什么公司" / "什么企业"
func detectAggregateIntent(question string) bool {
	lower := strings.ToLower(question)
	for _, w := range []string{"多少", "几个", "几条", "总数", "how many", "count", "统计", "有哪些", "哪些", "所有", "列出", "什么公司", "什么企业", "哪些公司", "哪些企业", "哪些合同"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// "什么X" pattern where X is an entity type (公司/企业/合同/项目/客户)
	if regexp.MustCompile(`什么(公司|企业|合同|项目|客户|人)`).MatchString(question) {
		return true
	}
	return false
}

// detectRelationIntent: "A和B什么关系" / "A与B如何关联"
func detectRelationIntent(question string) bool {
	if strings.Contains(question, "什么关系") || strings.Contains(question, "关系是") { return true }
	if regexp.MustCompile(`.+[和与跟].+`).MatchString(question) &&
		(strings.Contains(question, "关系") || strings.Contains(question, "关联") || strings.Contains(question, "联系")) {
		return true
	}
	return false
}

// handleTimeIntentAsk: timeline summary for date ranges.
func (srv *Server) handleTimeIntentAsk(w http.ResponseWriter, r *http.Request, dr *dateRange, question string) {
	items, err := srv.store.impl.List(store.ListOptions{CreatedAfter: &dr.from, CreatedBefore: &dr.to, Limit: 500})
	if err != nil || len(items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"answer": fmt.Sprintf("%s 没有记忆记录。", dr.label), "sources": []any{}})
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("以下是用户在 %s 的活动记录（共%d条）。请总结用户这段时间做了什么。用中文回答。\n\n", dr.label, len(items)))
	for i, m := range items {
		if i >= 100 { sb.WriteString(fmt.Sprintf("... 共 %d 条\n", len(items))); break }
		c := m.Content; if len(c) > 200 { c = c[:200] + "..." }
		role := m.Role; if role == "" { role = "note" }
		sb.WriteString(fmt.Sprintf("[%s][%s] %s\n", m.CreatedAt.Format("01-02 15:04"), role, c))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second); defer cancel()
	summary, err := srv.llm.Chat(ctx, sb.String())
	if err != nil { summary = fmt.Sprintf("%s 有 %d 条记录，但总结失败。", dr.label, len(items)) }
	writeJSON(w, http.StatusOK, map[string]any{"answer": summary, "sources": []any{}, "date": dr.label, "count": len(items)})
}

// handleAggregateIntent: count/list questions via SQL aggregation.
func (srv *Server) handleAggregateIntent(w http.ResponseWriter, r *http.Request, question string) {
	sqlStore, ok := srv.store.impl.(*store.SqliteStore)
	if !ok { return }
	db := sqlStore.DB()
	var sb strings.Builder
	sb.WriteString("根据记忆数据库的统计：\n\n**记忆总数**\n")
	rows, _ := db.Query("SELECT phase, COUNT(*) FROM memories GROUP BY phase ORDER BY COUNT(*) DESC")
	if rows != nil {
		for rows.Next() { var p string; var c int; rows.Scan(&p, &c); sb.WriteString(fmt.Sprintf("- %s: %d 条\n", p, c)) }
		rows.Close()
	}
	if strings.Contains(strings.ToLower(question), "项目") || strings.Contains(strings.ToLower(question), "project") {
		sb.WriteString("\n**项目分布**\n")
		prows, _ := db.Query("SELECT project, COUNT(*) FROM memories WHERE project != '' GROUP BY project ORDER BY COUNT(*) DESC LIMIT 20")
		if prows != nil { for prows.Next() { var p string; var c int; prows.Scan(&p, &c); sb.WriteString(fmt.Sprintf("- %s: %d 条\n", p, c)) }; prows.Close() }
	}
	if regexp.MustCompile(`(哪些|所有|列出|什么)`).MatchString(question) {
		for _, kw := range []string{"企业", "公司", "合同", "客户", "往来"} {
			if strings.Contains(question, kw) {
				sb.WriteString(fmt.Sprintf("\n**相关%s记录**\n", kw))
				erows, _ := db.Query("SELECT DISTINCT substr(content, 1, 80) FROM memories WHERE content LIKE ? AND content NOT LIKE '%<system-reminder>%' LIMIT 20", "%"+kw+"%")
				if erows != nil { cnt := 0; for erows.Next() { var c string; erows.Scan(&c); cnt++; sb.WriteString(fmt.Sprintf("%d. %s\n", cnt, c)) }; erows.Close() }
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": sb.String(), "sources": []any{}})
}

// handleRelationIntent: co-occurrence search for "A和B什么关系".
func (srv *Server) handleRelationIntent(w http.ResponseWriter, r *http.Request, question string) {
	var a, b string
	if m := regexp.MustCompile(`(.+?)(?:和|与|跟|and)(.+?)(?:什么|怎么|如何|有).*(?:关系|关联|联系)`).FindStringSubmatch(question); m != nil {
		a = strings.TrimSpace(m[1]); b = strings.TrimSpace(m[2])
	}
	if a == "" || b == "" { return }
	allMems, _ := srv.store.impl.List(store.ListOptions{Limit: 5000})
	var co []*store.Memory
	for _, m := range allMems {
		if strings.Contains(m.Content, a) && strings.Contains(m.Content, b) { co = append(co, m) }
	}
	if len(co) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"answer": fmt.Sprintf("没有找到 %s 和 %s 同时出现的记忆。", a, b), "sources": []any{}})
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("以下是同时提到 \"%s\" 和 \"%s\" 的记忆（共%d条）。请总结它们的关系。用中文回答。\n\n", a, b, len(co)))
	for i, m := range co {
		if i >= 20 { break }
		c := m.Content; if len(c) > 300 { c = c[:300] + "..." }
		sb.WriteString(fmt.Sprintf("[DATE: %s] %s\n", m.CreatedAt.Format("2006-01-02"), c))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second); defer cancel()
	ans, err := srv.llm.Chat(ctx, sb.String())
	if err != nil { ans = fmt.Sprintf("找到 %d 条共现记录，但总结失败。", len(co)) }
	writeJSON(w, http.StatusOK, map[string]any{"answer": ans, "sources": []any{}, "count": len(co)})
}

func (srv *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string         `json:"question"`
		History  []chatMessage  `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "missing question")
		return
	}

	// ── Intent dispatch ──
	// 1. Time intent: "今天/昨天/上周/近7天/6月15到20号"
	if dr, ok := detectTimeIntent(req.Question); ok {
		srv.handleTimeIntentAsk(w, r, dr, req.Question)
		return
	}
	// 2. Aggregate/list intent: "有多少记忆/有哪些项目/服务过哪些企业"
	if detectAggregateIntent(req.Question) {
		srv.handleAggregateIntent(w, r, req.Question)
		return
	}
	// 3. Relation intent: "A和B什么关系"
	if detectRelationIntent(req.Question) {
		srv.handleRelationIntent(w, r, req.Question)
		return
	}

	// Build a search query that includes context from recent conversation. A follow-up like
	// "有相关的时间记录吗？" is meaningless without the prior question ("juli landing 制作记录").
	// We prepend the last user turn's keywords to give the search context.
	searchInput := req.Question
	if len(req.History) > 0 {
		for i := len(req.History) - 1; i >= 0; i-- {
			if req.History[i].Role == "user" && req.History[i].Content != req.Question {
				searchInput = req.History[i].Content + " " + req.Question
				break
			}
		}
	}

	// Extract search keywords via LLM. This replaces the brittle regex+stopword approach:
	// the old extractSearchKeywords couldn't handle arbitrary Chinese questions because FTS5's
	// unicode61 tokenizer has no CJK segmentation. The LLM understands any phrasing and returns
	// clean keywords that FTS OR LIKE can match. Falls back to the raw question if LLM is down.
	searchQuery := searchInput
	if srv.llm != nil {
		if kw, err := llmExtractKeywords(r.Context(), srv.llm, searchInput); err == nil && kw != "" {
			searchQuery = kw
		}
	}

	// Search inbox FIRST — it has the most complete raw data (organized memories are
	// compressed summaries that may drop specific entities like company names). Then add
	// organized/processed as supplements. This prevents a case where organized returns
	// generic matches (e.g. "上海" matching 200 unrelated memories) while the real answer
	// (e.g. "瑞福莱" only in inbox) gets crowded out.
	// inbox uses SearchLike (IDF-ranked LIKE) — FTS OR on generic keywords like "上海"
	// returns hundreds of irrelevant matches. The Store interface now declares SearchLike,
	// so this works for both SqliteStore (real IDF ranking) and FileStore (fallback to Search).
	inbox, _ := srv.store.impl.SearchLike(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseInbox,
	})

	// Search organized + processed as supplements.
	results, err := srv.store.impl.Search(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseOrganized,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	processed, _ := srv.store.impl.Search(store.SearchOptions{
		Query: searchQuery,
		Phase: store.PhaseProcessed,
	})

	// Allocate slots: inbox gets priority (it has the real data). If inbox has many results,
	// give it more room and shrink organized.
	const procLimit = 4
	inboxLimit := 12
	orgLimit := 6
	if len(inbox) < 3 {
		// Inbox has little — lean more on organized summaries.
		inboxLimit = 6
		orgLimit = 12
	}
	if len(results) > orgLimit {
		results = results[:orgLimit]
	}
	if len(processed) > procLimit {
		processed = processed[:procLimit]
	}
	// Inbox: take IDF-ranked top N directly. Do NOT re-sort or spreadByDate — that would
	// destroy the IDF ranking and let common-keyword matches crowd out rare entities.
	if len(inbox) > inboxLimit {
		inbox = inbox[:inboxLimit]
	}
	results = append(results, processed...)
	results = append(results, inbox...)

	// Fallback: if current question found 0 results but there's conversation history,
	// re-search using the PREVIOUS user question's keywords. This handles follow-ups like
	// "具体的细节以及日期提供给我" which on their own extract generic keywords ("细节 OR 日期"),
	// but in context are asking about the prior topic (e.g. "瑞福莱的合同").
	if len(results) == 0 && len(req.History) > 0 && srv.llm != nil {
		for i := len(req.History) - 1; i >= 0; i-- {
			if req.History[i].Role == "user" {
				prevQ := req.History[i].Content
				if kw, err := llmExtractKeywords(r.Context(), srv.llm, prevQ); err == nil && kw != "" {
					// Re-run all three phase searches with previous keywords.
					results, _ = srv.store.impl.Search(store.SearchOptions{Query: kw, Phase: store.PhaseOrganized})
					proc2, _ := srv.store.impl.Search(store.SearchOptions{Query: kw, Phase: store.PhaseProcessed})
					inb2, _ := srv.store.impl.SearchLike(store.SearchOptions{Query: kw, Phase: store.PhaseInbox})
					if len(inb2) > inboxLimit {
						inb2 = inb2[:inboxLimit]
					}
					results = append(results, proc2...)
					results = append(results, inb2...)
					// Update searchQuery for snippet extraction.
					searchQuery = kw
				}
				break
			}
		}
	}

	if len(results) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":   "No relevant memories found for your question.",
			"sources":  []any{},
			"question": req.Question,
		})
		return
	}

	// Build context from memories. The DATE is the most important field for "when" questions,
	// so it goes first and most prominent on each line. Inbox items carry the real event date;
	// organized items carry the processing date (marked as such so the LLM doesn't confuse them).
	var sb strings.Builder
	// Parse keywords for snippet extraction (so we center the 300-char window on the
	// keyword, not the start of the message — "瑞福莱" may be at char 500 of a thinking block).
	snippetKWs := strings.Split(strings.ReplaceAll(strings.ToLower(searchQuery), " or ", "|"), "|")

	for i, m := range results {
		content := m.Content
		if len(content) > 300 {
			// Find the earliest keyword position and center the snippet around it.
			bestPos := 0
			contentLower := strings.ToLower(content)
			for _, kw := range snippetKWs {
				kw = strings.TrimSpace(kw)
				if kw == "" {
					continue
				}
				if idx := strings.Index(contentLower, kw); idx >= 0 {
					if bestPos == 0 || idx < bestPos {
						bestPos = idx
					}
				}
			}
			// Center a 300-char window on bestPos (clamp to bounds).
			start := bestPos - 80
			if start < 0 {
				start = 0
			}
			end := start + 300
			if end > len(content) {
				end = len(content)
			}
			content = content[start:end]
			if start > 0 {
				content = "..." + content
			}
			if end < len(m.Content) {
				content = content + "..."
			}
		}
		dateStr := m.CreatedAt.Format("2006-01-02")
		// Mark organized dates as processing-time so the LLM distinguishes them from event dates.
		if m.Phase == store.PhaseOrganized || m.Phase == store.PhaseProcessed {
			dateStr = "(processed " + dateStr + ")"
		}
		sb.WriteString(fmt.Sprintf("\n[%d] DATE: %s | %s", i+1, dateStr, content))
		sb.WriteString("\n")
	}

	// Compute the real date range from inbox items (the actual event timespan).
	var earliestDate, latestDate string
	for _, m := range inbox {
		d := m.CreatedAt.Format("2006-01-02")
		if earliestDate == "" || d < earliestDate {
			earliestDate = d
		}
		if latestDate == "" || d > latestDate {
			latestDate = d
		}
	}

	prompt := fmt.Sprintf(`You are a memory assistant. Answer the user's question based ONLY on the following memories.

CRITICAL — DATES: Each memory line starts with "DATE: YYYY-MM-DD". These are the real dates when things happened. The actual time span of this topic is %s to %s. When asked about timing, you MUST cite specific dates from the memories. Do NOT say "no date information" — the dates are right there at the start of each line. Memories marked "(processed YYYY-MM-DD)" are summaries — their dates are when the summary was generated, not when the event happened; prefer the plain dates.

User's memories:
%s

Question: %s

Answer concisely in the same language as the question:`, earliestDate, latestDate, sb.String(), req.Question)

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

// llmExtractKeywords asks the LLM to extract 2-5 search keywords from a natural-language
// question. This handles arbitrary Chinese/English phrasing that regex-based extraction
// can't — the LLM understands semantics and returns clean tokens.
func llmExtractKeywords(ctx context.Context, c *llm.Client, question string) (string, error) {
	prompt := fmt.Sprintf(`Extract 2-5 search keywords from this question for a full-text search engine.
Rules:
- Output the KEYWORDS ONLY, space-separated, no explanation
- Keep proper nouns intact (juli, RSA, GLM, makro)
- For Chinese, output individual meaningful words (企业, 客户, 部署), not whole phrases
- Remove question words (什么, 怎么, 吗, 呢, 的, 是, 还记得, 有没有)
- Mix English and Chinese as appropriate to the question

Question: %s

Keywords:`, question)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := c.ChatWithModel(ctx, "glm-4.7-flash", prompt, 100)
	if err != nil {
		return "", err
	}
	resp = strings.TrimSpace(resp)
	if idx := strings.IndexByte(resp, '\n'); idx > 0 {
		resp = resp[:idx]
	}
	resp = strings.Trim(resp, "`\"'.,")
	fields := strings.Fields(resp)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty keywords")
	}
	return strings.Join(fields, " OR "), nil
}

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
