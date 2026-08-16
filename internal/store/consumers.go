package store

// Consumer is a bitmask flag identifying a pipeline stage that consumes memories.
// Each consumer owns one bit in a memory's ConsumedMask. Tracking "has consumer X
// consumed memory Y" as a single integer column lets us mark consumption with one
// atomic statement (UPDATE mask = mask | bit) — no read-modify-write, so concurrent
// consumers can never lose each other's marks.
type Consumer uint64

const (
	// ConsumerFactProcessor extracts structured facts from inbox conversation logs
	// (inbox → processed). Driven by the plugin pipeline.
	ConsumerFactProcessor Consumer = 1 << iota // "fact-processor"
	// ConsumerConsolidateLLM merges related memories into denser organized summaries.
	ConsumerConsolidateLLM // "consolidate-llm"
	// ConsumerEnrichTags adds semantic concept/topic tags via the LLM enrichment task.
	ConsumerEnrichTags // "enrich-tags"
	// ConsumerProcessCmd marks inbox memories that the manual `memory process` command has
	// already extracted+merged. Lets re-runs skip them without touching the inbox phase.
	ConsumerProcessCmd // "process-cmd"
	// ConsumerEntityExtract extracts named entities from memory content (company names,
	// project names, tech terms) and populates the entity/entity_mentions tables. This is
	// NOT limited to [[wiki-links]] — it uses LLM to find entities in any text.
	ConsumerEntityExtract // "entity-extract"
	// ConsumerSessionDigest aggregates memories per session_id into the session_views
	// projection (task/entity/facet/summary/lessons). Marks memories it already fed into
	// a digest so the task only re-digests sessions with new material.
	ConsumerSessionDigest // "session-digest"
)

// consumerByName maps the string names used at the Store interface / pipeline boundary
// to their bitmask. Add a new consumer here (and as a const above) to track it.
var consumerByName = map[string]Consumer{
	"fact-processor":   ConsumerFactProcessor,
	"consolidate-llm":  ConsumerConsolidateLLM,
	"enrich-tags":      ConsumerEnrichTags,
	"process-cmd":      ConsumerProcessCmd,
	"entity-extract":   ConsumerEntityExtract,
	"session-digest":   ConsumerSessionDigest,
}

// ConsumerByName resolves a consumer's string name to its bitmask.
// Returns ok=false for unknown consumers (callers should treat that as a no-op).
func ConsumerByName(name string) (Consumer, bool) {
	c, ok := consumerByName[name]
	return c, ok
}

// IsConsumed reports whether the given consumer's bit is set on a memory's mask.
// Works on any loaded memory (whose ConsumedMask was scanned from the row).
func IsConsumed(mask int64, name string) bool {
	c, ok := ConsumerByName(name)
	if !ok {
		return false
	}
	return mask&int64(c) != 0
}

// IsConsumedByMemory is a convenience wrapper for a loaded *Memory.
func IsConsumedByMemory(mem *Memory, name string) bool {
	if mem == nil {
		return false
	}
	return IsConsumed(mem.ConsumedMask, name)
}
