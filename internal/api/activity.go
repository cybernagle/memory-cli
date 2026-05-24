package api

import (
	"net/http"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ss, ok := s.store.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"days": []any{}, "summary": nil})
		return
	}

	days := r.URL.Query().Get("days")
	if days == "" {
		days = "30"
	}

	data, err := getActivityData(ss, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ss, ok := s.store.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"days": []any{}, "summary": nil})
		return
	}

	data, err := getHeatmapData(ss)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func getActivityData(ss *store.SqliteStore, daysStr string) (map[string]any, error) {
	rows, err := ss.QueryRows(`
		SELECT date(created_at) as day, source, COUNT(*) as count
		FROM memories
		WHERE created_at >= datetime('now', '-' || ? || ' days')
		GROUP BY day, source
		ORDER BY day DESC`, daysStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type daySource struct {
		Day    string `json:"day"`
		Source string `json:"source"`
		Count  int    `json:"count"`
	}
	var items []daySource
	for rows.Next() {
		var d daySource
		if err := rows.Scan(&d.Day, &d.Source, &d.Count); err != nil {
			continue
		}
		items = append(items, d)
	}
	if items == nil {
		items = []daySource{}
	}
	return map[string]any{"days": items}, nil
}

func getHeatmapData(ss *store.SqliteStore) (map[string]any, error) {
	rows, err := ss.QueryRows(`
		SELECT date(created_at) as day, COUNT(*) as count
		FROM memories
		WHERE created_at >= datetime('now', '-365 days')
		GROUP BY day
		ORDER BY day ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dayCount struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
		Level int    `json:"level"`
	}
	var days []dayCount
	maxCount := 1
	total := 0
	streak := 0
	maxStreak := 0
	currentStreak := 0

	for rows.Next() {
		var d dayCount
		if err := rows.Scan(&d.Date, &d.Count); err != nil {
			continue
		}
		if d.Count > maxCount {
			maxCount = d.Count
		}
		total += d.Count
		days = append(days, d)
	}

	for i := range days {
		count := days[i].Count
		days[i].Level = countToLevel(count, maxCount)
		if count > 0 {
			currentStreak++
			if currentStreak > maxStreak {
				maxStreak = currentStreak
			}
		} else {
			currentStreak = 0
		}
	}

	today := time.Now().Format("2006-01-02")
	if len(days) > 0 && days[len(days)-1].Date == today {
		streak = maxStreak
	}

	avgDay := 0
	if len(days) > 0 {
		avgDay = total / len(days)
	}

	// By source breakdown (from memories, not activity_log)
	sourceRows, err := ss.QueryRows(`
		SELECT source, COUNT(*) as count
		FROM memories
		WHERE created_at >= datetime('now', '-30 days')
		GROUP BY source`)
	if err == nil {
		defer sourceRows.Close()
	}
	bySource := map[string]int{}
	if sourceRows != nil {
		for sourceRows.Next() {
			var source string
			var count int
			if sourceRows.Scan(&source, &count) == nil {
				bySource[source] = count
			}
		}
	}

	// By phase breakdown
	phaseRows, err := ss.QueryRows(`
		SELECT phase, COUNT(*) as count
		FROM memories
		WHERE created_at >= datetime('now', '-30 days')
		GROUP BY phase`)
	if err == nil {
		defer phaseRows.Close()
	}
	byAction := map[string]int{}
	if phaseRows != nil {
		for phaseRows.Next() {
			var phase string
			var count int
			if phaseRows.Scan(&phase, &count) == nil {
				byAction[phase] = count
			}
		}
	}

	return map[string]any{
		"days": days,
		"summary": map[string]any{
			"total":   total,
			"streak":  streak,
			"max_day": maxCount,
			"avg_day": avgDay,
		},
		"by_action": byAction,
		"by_source": bySource,
	}, nil
}

func countToLevel(count, max int) int {
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
