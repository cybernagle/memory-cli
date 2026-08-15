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

// EventStatsResponse: metrics over raw_entries — the append-only event log that is the
// source of truth every derived view (memories/tags/links/FTS) is rebuilt from. Whereas
// StatsResponse describes derived data, this describes the log itself: growth, source
// mix, provenance coverage, and the dedup ratio (how many ingests collapsed onto an
// existing event instead of appending a new one).
type EventStatsResponse struct {
	Total       int            `json:"total"`        // events in the log
	Memories    int            `json:"memories"`     // derived memories (unique contents)
	DedupAbsorbed int          `json:"dedup_absorbed"` // Total - Memories: ingests absorbed by content-hash dedup
	Recent24h   int            `json:"recent_24h"`   // events appended in the last 24h
	FirstEvent  string         `json:"first_event"`  // earliest ingested_at ("YYYY-MM-DD HH:MM:SS", UTC)
	LastEvent   string         `json:"last_event"`
	BySource    map[string]int `json:"by_source"`
	ByDay       []DayCount     `json:"by_day"`       // last 30 days, ascending
	Provenance  map[string]int `json:"provenance"`   // per-field: how many events carry it
}

// DayCount is one day of event ingest volume.
type DayCount struct {
	Day   string `json:"day"` // "YYYY-MM-DD"
	Count int    `json:"count"`
}

// ComputeEventStats aggregates the event log via SQL. FileStore has no event log, so it
// returns an empty response (the dashboard hides the section when total==0).
func ComputeEventStats(s statser) (*EventStatsResponse, error) {
	resp := &EventStatsResponse{
		BySource:   map[string]int{},
		ByDay:      []DayCount{},
		Provenance: map[string]int{},
	}
	sqlStore, ok := s.(*store.SqliteStore)
	if !ok {
		return resp, nil // FileStore: no raw_entries
	}
	db := sqlStore.DB()

	db.QueryRow("SELECT COUNT(*), MIN(ingested_at), MAX(ingested_at) FROM raw_entries").
		Scan(&resp.Total, &resp.FirstEvent, &resp.LastEvent)
	db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&resp.Memories)
	if resp.Total > resp.Memories {
		resp.DedupAbsorbed = resp.Total - resp.Memories
	}
	db.QueryRow("SELECT COUNT(*) FROM raw_entries WHERE ingested_at >= datetime('now', '-1 day')").Scan(&resp.Recent24h)

	if rows, err := db.Query("SELECT source, COUNT(*) FROM raw_entries GROUP BY source ORDER BY COUNT(*) DESC LIMIT 12"); err == nil {
		for rows.Next() {
			var src string
			var n int
			if rows.Scan(&src, &n) == nil {
				resp.BySource[src] = n
			}
		}
		rows.Close()
	}

	if rows, err := db.Query(`SELECT date(ingested_at) d, COUNT(*) FROM raw_entries
		WHERE ingested_at >= datetime('now', '-30 days')
		GROUP BY d ORDER BY d`); err == nil {
		for rows.Next() {
			var d string
			var n int
			if rows.Scan(&d, &n) == nil {
				resp.ByDay = append(resp.ByDay, DayCount{Day: d, Count: n})
			}
		}
		rows.Close()
	}

	// Provenance coverage: the fat-event upgrade is complete when every event carries
	// these; legacy events show as gaps that can never be backfilled (source never had it).
	for field, col := range map[string]string{
		"project": "project", "session": "session_id", "tmux": "tmux_session",
		"branch": "git_branch", "prompt": "prompt_id", "message": "message_uuid",
	} {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM raw_entries WHERE "+col+" != ''").Scan(&n)
		resp.Provenance[field] = n
	}
	return resp, nil
}
