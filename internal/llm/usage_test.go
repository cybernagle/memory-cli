package llm

import "testing"

func TestPromptHash(t *testing.T) {
	// deterministic: same input → same hash
	h1 := promptHash("car-agent question")
	h2 := promptHash("car-agent question")
	if h1 != h2 {
		t.Errorf("promptHash not deterministic: %q vs %q", h1, h2)
	}
	// different input → different hash (this is what makes dup detection content-aware)
	if promptHash("car-agent question") == promptHash("makro question") {
		t.Error("different prompts must hash differently")
	}
	// 16 hex chars (full 64-bit FNV)
	if len(h1) != 16 {
		t.Errorf("promptHash len = %d, want 16", len(h1))
	}
	// empty → empty (caller omits hash)
	if promptHash("") != "" {
		t.Error("empty prompt should hash to empty")
	}
}
