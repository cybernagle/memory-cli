package dashboard

import (
	"fmt"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type StatsResponse struct {
	Total        int            `json:"total"`
	Inbox        int            `json:"inbox"`
	Organized    int            `json:"organized"`
	Processed    int            `json:"processed"`
	Categories   map[string]int `json:"categories"`
	Sources      map[string]int `json:"sources"`
	Tags         map[string]int `json:"tags"`
	Projects     map[string]int `json:"projects"`
	Roles        map[string]int `json:"roles"`
	Phases       map[string]int `json:"phases"`
	Processors   map[string]int `json:"processors"` // consumed_mask breakdown: how many memories each processor has touched
	Recent24h    int            `json:"recent_24h"`
	ExpiringSoon int            `json:"expiring_soon"`
	// Additional dimensions surfaced on the dashboard (were returned by the DB but not shown).
	EntityKinds  map[string]int `json:"entity_kinds"`   // entity graph quality: concept/technology/domain/...
	Models       map[string]int           `json:"models"`         // which LLM produced this memory
	Sessions     map[string]int           `json:"sessions"`       // tmux_session provenance (top 25)
	Backlog      map[string]int           `json:"backlog"`        // unconsumed counts per processor phase (legacy, kept for compat)
	Consumption  []ConsumptionCell        `json:"consumption"`    // phase×processor consumption matrix
}

// ConsumptionCell is one cell of the phase×processor consumption matrix: of the N memories in
// `phase`, how many have been consumed by `processor`. The dashboard renders this as a matrix
// so you can see pipeline health at a glance (e.g. "inbox 100% fact-processed, but processed
// only 3.5% consolidated"). Because we never delete data (only mark consumed), the consumed
// rows stay available for other processors — this matrix shows that availability.
type ConsumptionCell struct {
	Phase     string `json:"phase"`      // inbox / processed / organized
	Processor string `json:"processor"`  // fact-processor / consolidate-llm / enrich-tags / entity-extract
	Consumed  int    `json:"consumed"`   // how many in this phase have this processor's bit set
	Total     int    `json:"total"`      // total memories in this phase
	Pct       string `json:"pct"`        // consumed/total as "NN.N%" (pre-formatted for the UI)
}

// statser is the store capability ComputeStats needs. Both SqliteStore and FileStore
// satisfy it; the SQL fast path type-asserts to *SqliteStore for raw DB access.
type statser interface {
	List(opts store.ListOptions) ([]*store.Memory, error)
}

// ComputeStats uses SQL aggregate queries when backed by SqliteStore — no memory rows are
// loaded into Go, only counts. Falls back to iteration for FileStore/tests.
func ComputeStats(s statser) (*StatsResponse, error) {
	if sqlStore, ok := s.(*store.SqliteStore); ok {
		return computeStatsSQL(sqlStore)
	}
	return computeStatsIter(s)
}

// computeStatsSQL runs aggregate COUNT queries — never loads memory rows into Go.
//
// TODO(code-review): most Rows.Scan / QueryRow.Scan errors here are ignored (best-effort
// dashboard stats). The QueryRow scans (Recent24h, ExpiringSoon, processor counts) are the
// cheapest to check and would surface DB-level corruption early, but adding checks changes
// the return semantics (currently always returns a partial response + nil error). Defer until
// a dashboard reliability requirement lands.
func computeStatsSQL(s *store.SqliteStore) (*StatsResponse, error) {
	db := s.DB()
	resp := &StatsResponse{
		Categories: make(map[string]int),
		Sources:    make(map[string]int),
		Tags:       make(map[string]int),
		Projects:   make(map[string]int),
		Roles:      make(map[string]int),
		Phases:     make(map[string]int),
		Processors: make(map[string]int),
		EntityKinds:  make(map[string]int),
		Models:       make(map[string]int),
		Sessions:     make(map[string]int),
		Backlog:      make(map[string]int),
	}

	// Total + phase breakdown in one query.
	phaseRows, err := db.Query("SELECT phase, COUNT(*) FROM memories GROUP BY phase")
	if err != nil {
		return nil, err
	}
	for phaseRows.Next() {
		var phase string
		var count int
		phaseRows.Scan(&phase, &count)
		resp.Phases[phase] = count
		resp.Total += count
		switch store.Phase(phase) {
		case store.PhaseInbox:
			resp.Inbox = count
		case store.PhaseOrganized:
			resp.Organized = count
		case store.PhaseProcessed:
			resp.Processed = count
		}
	}
	phaseRows.Close()

	// Processor breakdown via consumed_mask bits. Each processor owns a bit; counting how
	// many memories have that bit set shows the processing pipeline state.
	for name, bit := range map[string]int64{
		"fact-processor":  int64(store.ConsumerFactProcessor),
		"consolidate-llm": int64(store.ConsumerConsolidateLLM),
		"enrich-tags":     int64(store.ConsumerEnrichTags),
		"process-cmd":     int64(store.ConsumerProcessCmd),
	} {
		var count int
		// Parenthesize the bitwise-AND: in SQLite `&` binds looser than `!=`, so the unparenthesized
		// `consumed_mask & ? != 0` parses as `consumed_mask & (? != 0)` and collapses all four
		// counts to the same value. Group the mask test explicitly.
		db.QueryRow("SELECT COUNT(*) FROM memories WHERE (consumed_mask & ?) != 0", bit).Scan(&count)
		resp.Processors[name] = count
	}
	// Unconsumed = raw inbox that no processor has touched yet.
	var unconsumed int
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE consumed_mask = 0").Scan(&unconsumed)
	resp.Processors["unconsumed"] = unconsumed

	// Projects (top 25 by count).
	projRows, err := db.Query("SELECT project, COUNT(*) FROM memories WHERE project != '' GROUP BY project ORDER BY COUNT(*) DESC LIMIT 25")
	if err == nil {
		for projRows.Next() {
			var p string
			var c int
			projRows.Scan(&p, &c)
			resp.Projects[p] = c
		}
		projRows.Close()
	}

	// Categories.
	catRows, err := db.Query("SELECT category, COUNT(*) FROM memories GROUP BY category")
	if err == nil {
		for catRows.Next() {
			var c string
			var n int
			catRows.Scan(&c, &n)
			resp.Categories[c] = n
		}
		catRows.Close()
	}

	// Sources.
	srcRows, err := db.Query("SELECT source, COUNT(*) FROM memories WHERE source != '' GROUP BY source")
	if err == nil {
		for srcRows.Next() {
			var s string
			var n int
			srcRows.Scan(&s, &n)
			resp.Sources[s] = n
		}
		srcRows.Close()
	}

	// Roles (user/assistant/empty→unknown).
	roleRows, err := db.Query("SELECT CASE WHEN role='' THEN 'unknown' ELSE role END, COUNT(*) FROM memories GROUP BY CASE WHEN role='' THEN 'unknown' ELSE role END")
	if err == nil {
		for roleRows.Next() {
			var r string
			var n int
			roleRows.Scan(&r, &n)
			resp.Roles[r] = n
		}
		roleRows.Close()
	}

	// Tags (top 25).
	tagRows, err := db.Query("SELECT tag, COUNT(*) FROM tags GROUP BY tag ORDER BY COUNT(*) DESC LIMIT 25")
	if err == nil {
		for tagRows.Next() {
			var t string
			var n int
			tagRows.Scan(&t, &n)
			resp.Tags[t] = n
		}
		tagRows.Close()
	}

	// Recent 24h + expiring soon — these are cheap COUNT queries, no row loading.
	dayAgo := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE created_at > ?", dayAgo).Scan(&resp.Recent24h)

	now := time.Now().Format(time.RFC3339)
	tomorrow := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE expires_at IS NOT NULL AND expires_at != '' AND expires_at > ? AND expires_at < ?", now, tomorrow).Scan(&resp.ExpiringSoon)

	// Entity kinds: how the entity graph breaks down by type (concept/technology/domain/...).
	// Surfaces the "96% concept" classification skew without needing the graph view.
	kindRows, err := db.Query("SELECT kind, COUNT(*) FROM entities GROUP BY kind ORDER BY COUNT(*) DESC")
	if err == nil {
		for kindRows.Next() {
			var k string
			var n int
			kindRows.Scan(&k, &n)
			resp.EntityKinds[k] = n
		}
		kindRows.Close()
	}

	// Models: which LLM produced these memories (glm-4.5-flash / glm-4.7-flash / ...).
	modelRows, err := db.Query("SELECT model, COUNT(*) FROM memories WHERE model != '' GROUP BY model ORDER BY COUNT(*) DESC")
	if err == nil {
		for modelRows.Next() {
			var m string
			var n int
			modelRows.Scan(&m, &n)
			resp.Models[m] = n
		}
		modelRows.Close()
	}

	// Sessions: tmux_session provenance (top 25). Shows which conversation context memories
	// originated from — the "where did this come from" dimension.
	sessRows, err := db.Query("SELECT tmux_session, COUNT(*) FROM memories WHERE tmux_session != '' GROUP BY tmux_session ORDER BY COUNT(*) DESC LIMIT 25")
	if err == nil {
		for sessRows.Next() {
			var ss string
			var n int
			sessRows.Scan(&ss, &n)
			resp.Sessions[ss] = n
		}
		sessRows.Close()
	}

	// Backlog + Consumption matrix: for each (phase × processor), how many are consumed vs
	// total. Backlog is the legacy unconsumed-on-organized/processed view; Consumption is the
	// full phase×processor matrix that lets you see e.g. "inbox 100% fact-processed".
	// Build both in one pass — they read the same underlying consumed_mask data.
	consumers := []struct {
		name string
		bit  int64
	}{
		{"fact-processor", int64(store.ConsumerFactProcessor)},
		{"consolidate-llm", int64(store.ConsumerConsolidateLLM)},
		{"enrich-tags", int64(store.ConsumerEnrichTags)},
		{"entity-extract", int64(store.ConsumerEntityExtract)},
	}
	// A processor only meaningfully consumes certain phases (e.g. fact-processor reads inbox,
	// consolidate/entity read processed/organized). Define which phase×processor cells to compute.
	matrix := []struct{ phase, processor string }{
		{"inbox", "fact-processor"},
		{"processed", "consolidate-llm"},
		{"processed", "enrich-tags"},
		{"processed", "entity-extract"},
		{"organized", "consolidate-llm"},
		{"organized", "enrich-tags"},
		{"organized", "entity-extract"},
	}
	bitFor := func(name string) int64 {
		for _, c := range consumers {
			if c.name == name {
				return c.bit
			}
		}
		return 0
	}
	for _, cell := range matrix {
		bit := bitFor(cell.processor)
		var total, consumed int
		db.QueryRow("SELECT COUNT(*) FROM memories WHERE phase = ?", cell.phase).Scan(&total)
		db.QueryRow("SELECT COUNT(*) FROM memories WHERE phase = ? AND (consumed_mask & ?) != 0",
			cell.phase, bit).Scan(&consumed)
		pct := "0.0"
		if total > 0 {
			pct = fmt.Sprintf("%.1f", float64(consumed)/float64(total)*100)
		}
		resp.Consumption = append(resp.Consumption, ConsumptionCell{
			Phase: cell.phase, Processor: cell.processor,
			Consumed: consumed, Total: total, Pct: pct,
		})
		// Backlog (legacy): unconsumed organized/processed per processor.
		if (cell.phase == "organized" || cell.phase == "processed") && consumed < total {
			resp.Backlog[cell.processor] += total - consumed
		}
	}

	return resp, nil
}

// computeStatsIter is the fallback for non-SQLite stores (FileStore, tests). Iterates all
// memories in Go — correct but slow at scale. Kept for compatibility.
func computeStatsIter(s statser) (*StatsResponse, error) {
	all, err := s.List(store.ListOptions{})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	dayAgo := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)

	resp := &StatsResponse{
		Total:      len(all),
		Categories: make(map[string]int),
		Sources:    make(map[string]int),
		Tags:       make(map[string]int),
		Projects:   make(map[string]int),
		Roles:      make(map[string]int),
		Phases:     make(map[string]int),
		Processors: make(map[string]int),
	}

	for _, mem := range all {
		resp.Phases[string(mem.Phase)]++
		switch mem.Phase {
		case store.PhaseInbox:
			resp.Inbox++
		case store.PhaseOrganized:
			resp.Organized++
		case store.PhaseProcessed:
			resp.Processed++
		}
		resp.Categories[string(mem.Category)]++
		if mem.Source != "" {
			resp.Sources[mem.Source]++
		}
		if mem.Project != "" {
			resp.Projects[mem.Project]++
		}
		role := mem.Role
		if role == "" {
			role = "unknown"
		}
		resp.Roles[role]++
		for _, tag := range mem.Tags {
			resp.Tags[tag]++
		}
		if mem.CreatedAt.After(dayAgo) {
			resp.Recent24h++
		}
		if mem.ExpiresAt != nil && !mem.ExpiresAt.IsZero() && mem.ExpiresAt.After(now) && mem.ExpiresAt.Before(tomorrow) {
			resp.ExpiringSoon++
		}
	}

	return resp, nil
}
