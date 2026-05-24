package api

import (
	"net/http"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

func (s *Server) handleHistogram(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	daysStr := r.URL.Query().Get("days")
	if daysStr == "" {
		daysStr = "90"
	}

	ss, ok := s.store.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"days": []any{}})
		return
	}

	rows, err := ss.QueryRows(`
		SELECT
			date(created_at) as day,
			COUNT(*) as total,
			SUM(CASE WHEN phase = 'inbox' THEN 1 ELSE 0 END) as inbox_count,
			SUM(CASE WHEN phase = 'organized' THEN 1 ELSE 0 END) as upgraded_count
		FROM memories
		GROUP BY date(created_at)
		ORDER BY day ASC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type dayData struct {
		Date     string `json:"date"`
		Total    int    `json:"total"`
		Inbox    int    `json:"inbox"`
		Upgraded int    `json:"upgraded"`
	}
	var days []dayData
	for rows.Next() {
		var d dayData
		if err := rows.Scan(&d.Date, &d.Total, &d.Inbox, &d.Upgraded); err != nil {
			continue
		}
		d.Upgraded = d.Total - d.Inbox
		days = append(days, d)
	}
	if days == nil {
		days = []dayData{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

func (s *Server) handleAggregation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	daysStr := r.URL.Query().Get("days")
	if daysStr == "" {
		daysStr = "30"
	}

	ss, ok := s.store.(*store.SqliteStore)
	if !ok {
		// Fallback: compute from memory list
		s.handleAggregationFallback(w, r, daysStr)
		return
	}

	rows, err := ss.QueryRows(`
		SELECT
			date(created_at) as day,
			SUM(CASE WHEN phase = 'inbox' THEN 1 ELSE 0 END) as inbox,
			SUM(CASE WHEN phase = 'organized' THEN 1 ELSE 0 END) as upgraded
		FROM memories
		WHERE created_at >= datetime('now', '-` + daysStr + ` days')
		GROUP BY date(created_at)
		ORDER BY day ASC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type aggDay struct {
		Date     string `json:"date"`
		Inbox    int    `json:"inbox"`
		Upgraded int    `json:"upgraded"`
	}
	var days []aggDay
	for rows.Next() {
		var d aggDay
		if err := rows.Scan(&d.Date, &d.Inbox, &d.Upgraded); err != nil {
			continue
		}
		days = append(days, d)
	}
	if days == nil {
		days = []aggDay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

func (s *Server) handleAggregationFallback(w http.ResponseWriter, r *http.Request, daysStr string) {
	daysAgo := 30
	// parse daysStr if needed
	_ = daysStr

	since := time.Now().AddDate(0, 0, -daysAgo)
	memories, err := s.store.List(store.ListOptions{CreatedAfter: &since})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type aggDay struct {
		Date     string `json:"date"`
		Inbox    int    `json:"inbox"`
		Upgraded int    `json:"upgraded"`
	}
	dayMap := map[string]*aggDay{}
	for _, m := range memories {
		day := m.CreatedAt.Format("2006-01-02")
		d, ok := dayMap[day]
		if !ok {
			d = &aggDay{Date: day}
			dayMap[day] = d
		}
		if m.Phase == store.PhaseInbox {
			d.Inbox++
		} else {
			d.Upgraded++
		}
	}

	var days []aggDay
	for _, d := range dayMap {
		days = append(days, *d)
	}
	if days == nil {
		days = []aggDay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}
