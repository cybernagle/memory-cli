package agent

import (
	"github.com/cybernagle/memory-cli/internal/store"
)

func RegisterAll(a *Agent, s store.Store) {
	a.RegisterTool(&MemoryWriteTool{store: s})
	a.RegisterTool(&MemoryReadTool{store: s})
	a.RegisterTool(&MemoryDeleteTool{store: s})
	a.RegisterTool(&MemoryListTool{store: s})
	a.RegisterTool(&MemorySearchTool{store: s})
	a.RegisterTool(&MemorySmartSearchTool{store: s})
	a.RegisterTool(&MemoryTagTool{store: s})
}
