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
)

// consumerByName maps the string names used at the Store interface / pipeline boundary
// to their bitmask. Add a new consumer here (and as a const above) to track it.
var consumerByName = map[string]Consumer{
	"fact-processor":  ConsumerFactProcessor,
	"consolidate-llm": ConsumerConsolidateLLM,
	"enrich-tags":     ConsumerEnrichTags,
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
