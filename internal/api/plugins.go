package api

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/store"
)

func (s *Server) handlePluginComponents(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"components": []any{}})
		return
	}
	comps := s.registry.AllComponents()
	type compInfo struct {
		Name           string `json:"name"`
		DataType       string `json:"data_type"`
		DecayStrategy  string `json:"decay_strategy"`
		HalfLife       string `json:"half_life"`
		NeverPurge     bool   `json:"never_purge"`
		Tables         int    `json:"tables"`
	}
	result := make([]compInfo, len(comps))
	for i, c := range comps {
		dp := c.DecayPolicy()
		result[i] = compInfo{
			Name:          c.Name(),
			DataType:      string(c.DataType()),
			DecayStrategy: dp.Strategy,
			HalfLife:      dp.HalfLife,
			NeverPurge:    dp.NeverPurge,
			Tables:        len(c.Schema()),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": result})
}

func (s *Server) handlePluginProcessors(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"processors": []any{}})
		return
	}
	procs := s.registry.AllProcessors()
	type procInfo struct {
		Name      string   `json:"name"`
		Consumes  []string `json:"consumes"`
		Produces  []string `json:"produces"`
	}
	result := make([]procInfo, len(procs))
	for i, p := range procs {
		result[i] = procInfo{
			Name:     p.Name(),
			Consumes: dataTypeStrings(p.Consumes()),
			Produces: dataTypeStrings(p.Produces()),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"processors": result})
}

func (s *Server) handlePluginIngests(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ingests": []any{}})
		return
	}
	ingests := s.registry.AllIngests()
	type ingestInfo struct {
		Name string `json:"name"`
	}
	result := make([]ingestInfo, len(ingests))
	for i, ing := range ingests {
		result[i] = ingestInfo{Name: ing.Name()}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingests": result})
}

func (s *Server) handlePluginEntities(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := parseInt(l); err == nil && n > 0 {
			limit = n
		}
	}

	ss, ok := s.store.(*store.SqliteStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"entities": []any{}})
		return
	}

	rows, err := ss.DB().Query(`
		SELECT e.name, e.kind, COUNT(m.id) as mention_count
		FROM entities e
		LEFT JOIN entity_mentions m ON e.id = m.entity_id
		GROUP BY e.id
		ORDER BY mention_count DESC
		LIMIT ?
	`, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entities": []any{}})
		return
	}
	defer rows.Close()

	type entityInfo struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		MentionCount int    `json:"mention_count"`
	}
	var result []entityInfo
	for rows.Next() {
		var e entityInfo
		if err := rows.Scan(&e.Name, &e.Kind, &e.MentionCount); err != nil {
			continue
		}
		result = append(result, e)
	}
	if result == nil {
		result = []entityInfo{}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MentionCount > result[j].MentionCount
	})
	writeJSON(w, http.StatusOK, map[string]any{"entities": result})
}

func dataTypeStrings(dt []plugin.DataType) []string {
	s := make([]string, len(dt))
	for i, d := range dt {
		s[i] = string(d)
	}
	return s
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}
