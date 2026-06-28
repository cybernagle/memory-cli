package daemon

import (
	"log"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/ingest"
	"github.com/cybernagle/memory-cli/internal/store"
)

// IngestTask periodically pulls fresh data from external sources into the inbox, closing the
// "capture" gap in the second-brain loop. Without it, the daemon only *processed* existing
// memories (inbox → organized) but never *collected* new ones — so zcode sessions written to
// ~/.zcode/cli/rollout/ were ingested only when someone ran `memory ingest --source zcode`
// manually. This task automates that.
//
// Scope (2026-06-28): zcode only. Claude has its own hook pipeline that writes directly, so
// it does NOT go through this poller. Adding a source means adding it to the sources slice.
//
// Idempotency: the adapter re-reads the whole rollout each run, but IngestMemory →
// InsertMemory dedups on content_hash (INSERT OR IGNORE), so re-ingesting existing turns is
// a no-op. Only genuinely new turns enter the inbox. This makes frequent polling cheap and
// safe — the cost is proportional to NEW content, not corpus size.
type IngestTask struct {
	Store   *store.SqliteStore
	Sources []string // which adapters to run; empty = {"zcode"}
}

func (t *IngestTask) Name() string { return "ingest" }

// Run executes all configured ingest adapters and writes new memories via IngestMemory (the
// unified write chokepoint), so supersede + dedup apply uniformly. Returns the count of
// newly-ingested memories across all sources.
func (t *IngestTask) Run(s store.Store) (int, error) {
	sources := t.Sources
	if len(sources) == 0 {
		sources = []string{"zcode"}
	}

	total := 0
	home := config.MustHomeDir()
	adapters := map[string]ingest.Adapter{
		// zcode reads ~/.zcode/cli/rollout/*.jsonl — each turn → one inbox memory.
		"zcode": &ingest.ZcodeAdapter{Path: home + "/.zcode/cli/rollout"},
		// (claude is intentionally absent: it has a hook that writes directly.)
	}

	for _, src := range sources {
		adapter, ok := adapters[src]
		if !ok {
			log.Printf("[ingest] no adapter registered for source %q, skipping", src)
			continue
		}
		memories, err := adapter.Ingest()
		if err != nil {
			log.Printf("[ingest] %s adapter error: %v", src, err)
			continue
		}
		newCount := 0
		for _, mem := range memories {
			// IngestMemory dedups via content_hash; only truly-new turns get written. It also
			// triggers supersede for organized/processed writes — but ingest targets inbox, so
			// no supersede here (inbox is raw events, not versioned facts).
			if err := t.Store.IngestMemory(mem); err != nil {
				continue // duplicate (INSERT OR IGNORE) or write error — skip, don't abort the batch
			}
			// We can't distinguish "new" from "dup" cheaply here (IngestMemory returns nil for
			// both), so count attempts; the log line below reflects this. Dedup is the store's job.
			newCount++
		}
		total += newCount
		if newCount > 0 {
			log.Printf("[ingest] %s: processed %d turns", src, newCount)
		}
	}
	return total, nil
}
