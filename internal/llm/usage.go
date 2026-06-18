package llm

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	// Reuse memory-cli's existing pure-Go SQLite driver (no new dependency).
	_ "modernc.org/sqlite"
)

// promptUsageDBPath is the shared prompt-usage log. It lives in ~/.makro/ so memory-cli
// and the Makro app write to the same file; session_name distinguishes the source.
func promptUsageDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".makro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompt_usage.db"), nil
}

const promptUsageSchema = `
CREATE TABLE IF NOT EXISTS prompt_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    session_name TEXT NOT NULL,
    model_type TEXT NOT NULL,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    call_function TEXT,
    call_context TEXT,
    is_duplicate BOOLEAN DEFAULT 0,
    call_duration INTEGER,
    error TEXT
);
CREATE INDEX IF NOT EXISTS idx_session_time ON prompt_usage(session_name, timestamp);
CREATE INDEX IF NOT EXISTS idx_model_time ON prompt_usage(model_type, timestamp);
CREATE INDEX IF NOT EXISTS idx_func_time ON prompt_usage(call_function, timestamp);
`

// UsageEntry is one LLM call's accounting, persisted to prompt_usage.
type UsageEntry struct {
	Session          string // "memory" for memory-cli
	Model            string
	Function         string // Extract / Merge / Chat / ConceptTags
	Context          string // caller-supplied label (e.g. "contents=12")
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	DurationMs       int64
	Error            string
}

var (
	usageOnce sync.Once
	usageDB   *sql.DB
	usageErr  error
)

func openUsageDB() (*sql.DB, error) {
	path, err := promptUsageDBPath()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2) // tiny write-mostly log; keep it lean
	if _, err := db.Exec(promptUsageSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Frequency-detection thresholds (per call_function).
const (
	dupWindow       = 5 * time.Minute
	freqWindowShort = 1 * time.Minute
	freqWindowLong  = 5 * time.Minute
	freqLimitShort  = 10 // >10 calls in 1 min = over-frequency
	freqLimitLong   = 30 // >30 calls in 5 min = over-frequency
)

// isDuplicate reports whether the same session+function+context was already recorded
// within the duplicate window (5 min). Uses the indexed (call_function, timestamp) scan.
func isDuplicate(db *sql.DB, session, fn, ctx string) bool {
	var one int
	err := db.QueryRow(
		`SELECT 1 FROM prompt_usage
		  WHERE session_name = ? AND call_function = ? AND call_context = ?
		    AND timestamp >= datetime('now', ?)
		  LIMIT 1`,
		session, fn, ctx, "-"+fmt.Sprintf("%d minutes", int(dupWindow.Minutes())),
	).Scan(&one)
	return err == nil // a row exists → duplicate
}

// checkFrequency logs a warning if a function crosses the over-frequency thresholds.
func checkFrequency(db *sql.DB, fn string) {
	countSince := func(dur time.Duration) int64 {
		var n int64
		db.QueryRow(
			`SELECT COUNT(*) FROM prompt_usage
			  WHERE call_function = ? AND timestamp >= datetime('now', ?)`,
			fn, "-"+fmt.Sprintf("%d minutes", int(dur.Minutes())),
		).Scan(&n)
		return n
	}
	if n := countSince(freqWindowShort); n > int64(freqLimitShort) {
		log.Printf("[prompt-usage] ⚠ OVER-FREQUENCY: %s called %d times in the last minute (limit %d)", fn, n, freqLimitShort)
	} else if n := countSince(freqWindowLong); n > int64(freqLimitLong) {
		log.Printf("[prompt-usage] ⚠ OVER-FREQUENCY: %s called %d times in the last 5 minutes (limit %d)", fn, n, freqLimitLong)
	}
}

// Record persists a usage entry. Best-effort: a logging failure never breaks the LLM
// call flow (it only logs). Lazy-initializes the shared DB on first call.
// Phase 2: flags duplicates (same session+function+context within 5 min) and checks
// over-frequency thresholds after each insert.
func Record(e UsageEntry) {
	usageOnce.Do(func() { usageDB, usageErr = openUsageDB() })
	if usageErr != nil || usageDB == nil {
		return
	}

	dup := 0
	if isDuplicate(usageDB, e.Session, e.Function, e.Context) {
		dup = 1
	}

	_, err := usageDB.Exec(
		`INSERT INTO prompt_usage
		   (session_name, model_type, prompt_tokens, completion_tokens, total_tokens,
		    call_function, call_context, is_duplicate, call_duration, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Session, e.Model, e.PromptTokens, e.CompletionTokens, e.TotalTokens,
		e.Function, e.Context, dup, e.DurationMs, e.Error,
	)
	if err != nil {
		log.Printf("[prompt-usage] record failed: %v", err)
		return
	}

	// Over-frequency detection (cheap indexed counts; LLM calls are slow so this is negligible).
	checkFrequency(usageDB, e.Function)
}

// tokenUsageFrom returns the token counts from a chat result. Both are zero when the
// result is nil (e.g. the call errored before returning).
func tokenUsageFrom(result *chatResult) (prompt, completion int64) {
	if result == nil {
		return 0, 0
	}
	return result.promptTokens, result.completionTokens
}

// recordLLMCall is the single hook every LLM method calls after its request.
// It captures tokens (when available), duration, and errors, then persists them.
// The stored call_context combines a human label with a short hash of the prompt, so
// duplicate detection (same function+context within 5 min) is content-aware — two asks
// with different questions are NOT duplicates even if they share maxTokens.
func recordLLMCall(fnName, model, prompt, label string, result *chatResult, err error, duration time.Duration) {
	ctx := label
	if h := promptHash(prompt); h != "" {
		ctx = label + " h=" + h
	}
	if err != nil {
		Record(UsageEntry{
			Session: "memory", Model: model, Function: fnName, Context: ctx,
			DurationMs: duration.Milliseconds(), Error: fmt.Sprintf("%v", err),
		})
		return
	}
	inTok, outTok := tokenUsageFrom(result)
	Record(UsageEntry{
		Session: "memory", Model: model, Function: fnName, Context: ctx,
		PromptTokens: inTok, CompletionTokens: outTok, TotalTokens: inTok + outTok,
		DurationMs: duration.Milliseconds(),
	})
}

// promptHash returns a short stable fingerprint of a prompt (first 8 hex chars of FNV-1a).
// Empty for empty input. Used only to distinguish calls — not security-sensitive.
func promptHash(prompt string) string {
	if prompt == "" {
		return ""
	}
	const offset64 uint64 = 1469598103934665603
	const prime64 uint64 = 1099511628211
	h := offset64
	for i := 0; i < len(prompt); i++ {
		h ^= uint64(prompt[i])
		h *= prime64
	}
	return fmt.Sprintf("%08x", h&0xFFFFFFFF) // mask to 32 bits → exactly 8 hex chars
}

// UsageStat is a per-dimension (function or model) usage rollup.
type UsageStat struct {
	Key        string
	Count      int64
	Prompt     int64
	Completion int64
	Total      int64
}

// UsageReport aggregates prompt_usage for display (the `memory usage` command).
type UsageReport struct {
	TotalCalls       int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Duplicates       int64
	Errors           int64
	ByFunction       []UsageStat
	ByModel          []UsageStat
	OverFreq         []string // functions over a frequency threshold in the recent window
	Recent           []UsageEntry
}

// QueryReport reads aggregate usage stats from the shared DB. Safe to call when no DB
// exists yet (returns a zero report + nil error).
func QueryReport(recentLimit int) (*UsageReport, error) {
	usageOnce.Do(func() { usageDB, usageErr = openUsageDB() })
	r := &UsageReport{}
	if usageErr != nil || usageDB == nil {
		return r, nil
	}

	// Totals + duplicate/error counts.
	err := usageDB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		        COALESCE(SUM(total_tokens),0), COALESCE(SUM(is_duplicate),0),
		        COALESCE(SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END),0)
		   FROM prompt_usage`).Scan(
		&r.TotalCalls, &r.PromptTokens, &r.CompletionTokens, &r.TotalTokens, &r.Duplicates, &r.Errors)
	if err != nil {
		return nil, err
	}

	r.ByFunction, _ = queryStats("call_function")
	r.ByModel, _ = queryStats("model_type")
	r.OverFreq = queryOverFreq()

	if recentLimit > 0 {
		rows, err := usageDB.Query(
			`SELECT COALESCE(session_name,''), COALESCE(model_type,''), COALESCE(call_function,''),
			        COALESCE(call_context,''), COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
			        COALESCE(total_tokens,0), COALESCE(call_duration,0), COALESCE(error,'')
			   FROM prompt_usage ORDER BY id DESC LIMIT ?`, recentLimit)
		if err == nil {
			for rows.Next() {
				var e UsageEntry
				rows.Scan(&e.Session, &e.Model, &e.Function, &e.Context, &e.PromptTokens,
					&e.CompletionTokens, &e.TotalTokens, &e.DurationMs, &e.Error)
				r.Recent = append(r.Recent, e)
			}
			rows.Close()
		}
	}
	return r, nil
}

func queryStats(col string) ([]UsageStat, error) {
	rows, err := usageDB.Query(
		`SELECT ` + col + `, COUNT(*), COALESCE(SUM(prompt_tokens),0),
		        COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
		   FROM prompt_usage GROUP BY ` + col + ` ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageStat
	for rows.Next() {
		var s UsageStat
		rows.Scan(&s.Key, &s.Count, &s.Prompt, &s.Completion, &s.Total)
		out = append(out, s)
	}
	return out, nil
}

// queryOverFreq returns functions currently exceeding a frequency threshold.
func queryOverFreq() []string {
	var out []string
	rows, err := usageDB.Query(
		`SELECT call_function, COUNT(*) c FROM prompt_usage
		  WHERE timestamp >= datetime('now','-1 minute')
		  GROUP BY call_function HAVING c > ?
		  UNION
		  SELECT call_function, COUNT(*) c FROM prompt_usage
		  WHERE timestamp >= datetime('now','-5 minutes')
		  GROUP BY call_function HAVING c > ?`,
		freqLimitShort, freqLimitLong)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var fn string
		var c int64
		rows.Scan(&fn, &c)
		out = append(out, fmt.Sprintf("%s (%d recent)", fn, c))
	}
	return out
}
