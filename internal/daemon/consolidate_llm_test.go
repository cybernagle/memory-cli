package daemon

import (
	"testing"

	"github.com/cybernagle/memory-cli/internal/store"
)

func TestGroupByProject(t *testing.T) {
	mems := []*store.Memory{
		{ID: "1", Project: "makro"},
		{ID: "2", Project: "makro"},
		{ID: "3", Project: "car-agent"},
		{ID: "4", Project: ""},
		{ID: "5", Project: "makro"},
	}
	groups := groupByProject(mems)

	if len(groups["makro"]) != 3 {
		t.Errorf("makro group = %d, want 3", len(groups["makro"]))
	}
	if len(groups["car-agent"]) != 1 {
		t.Errorf("car-agent group = %d, want 1", len(groups["car-agent"]))
	}
	if len(groups["(none)"]) != 1 {
		t.Errorf("(none) group = %d, want 1 (empty project bucketed)", len(groups["(none)"]))
	}
}

func TestSetProjectOnMemoryNoOpForNone(t *testing.T) {
	// "(none)" and "" must not be written as a project; only real project names are.
	// (We can't easily open a store here, but the guard is pure logic — verify it doesn't panic
	// by passing a nil store for the no-op cases, which return before touching the DB.)
	setProjectOnMemory(nil, "x", "(none)") // must not panic (returns early)
	setProjectOnMemory(nil, "x", "")
}
