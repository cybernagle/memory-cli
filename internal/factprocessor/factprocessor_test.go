package factprocessor

import (
	"testing"

	"github.com/cybernagle/memory-cli/internal/plugin"
)

// TestNewMemoryFromDataItemReusesID verifies the entity-fix ID threading:
// when the fact processor pre-generates an id (so ResolveMentions can link
// mentions to it), NewMemoryFromDataItem must reuse that SAME id rather than
// generating a new one. Otherwise entity_mentions would point at a non-existent id.
func TestNewMemoryFromDataItemReusesID(t *testing.T) {
	item := plugin.DataItem{
		DataType: plugin.DataEntity,
		Data: map[string]interface{}{
			"id":       "preset-id-abc",
			"content":  "User prefers [[Go]] for backend services",
			"category": "knowledge",
			"tags":     []string{"backend"},
		},
	}
	mem := NewMemoryFromDataItem(item)
	if mem.ID != "preset-id-abc" {
		t.Errorf("ID = %q, want preset-id-abc (must reuse provided id so mentions link correctly)", mem.ID)
	}
	if mem.Content != "User prefers [[Go]] for backend services" {
		t.Errorf("content mismatch: %q", mem.Content)
	}
}

// TestNewMemoryFromDataItemGeneratesIDWhenMissing verifies the fallback:
// if no id is threaded through (e.g. other producers), a fresh id is generated.
func TestNewMemoryFromDataItemGeneratesIDWhenMissing(t *testing.T) {
	mem := NewMemoryFromDataItem(plugin.DataItem{
		Data: map[string]interface{}{"content": "no id here"},
	})
	if mem.ID == "" {
		t.Fatal("expected a generated id when none provided")
	}
}
