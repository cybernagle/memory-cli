package store

import "testing"

func TestConsumerByNameAndIsConsumed(t *testing.T) {
	cases := map[string]Consumer{
		"fact-processor":  ConsumerFactProcessor,
		"consolidate-llm": ConsumerConsolidateLLM,
		"enrich-tags":     ConsumerEnrichTags,
	}
	for name, want := range cases {
		got, ok := ConsumerByName(name)
		if !ok {
			t.Errorf("ConsumerByName(%q): unknown", name)
			continue
		}
		if got != want {
			t.Errorf("ConsumerByName(%q) = %d, want %d", name, got, want)
		}
		// each consumer must be a distinct single bit
		if got == 0 || got&(got-1) != 0 {
			t.Errorf("consumer %q mask %d is not a single bit", name, got)
		}
	}
	if _, ok := ConsumerByName("unknown-processor"); ok {
		t.Error("unknown consumer should resolve ok=false")
	}
}

func TestIsConsumed(t *testing.T) {
	// fact-processor (bit 0) + enrich-tags (bit 2) set, consolidate-llm (bit 1) not
	mask := int64(ConsumerFactProcessor | ConsumerEnrichTags)
	if !IsConsumed(mask, "fact-processor") {
		t.Error("fact-processor should be consumed")
	}
	if !IsConsumed(mask, "enrich-tags") {
		t.Error("enrich-tags should be consumed")
	}
	if IsConsumed(mask, "consolidate-llm") {
		t.Error("consolidate-llm should NOT be consumed")
	}
	if IsConsumed(mask, "unknown") {
		t.Error("unknown consumer must report not-consumed")
	}
}
