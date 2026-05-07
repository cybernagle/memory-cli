package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type FingersaverAdapter struct {
	Path string
}

func (a *FingersaverAdapter) Name() string { return "fingersaver" }

func (a *FingersaverAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".fingersaver")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}
	var memories []*store.Memory

	chatFile := filepath.Join(a.Path, "chat.md")
	if data, err := os.ReadFile(chatFile); err == nil {
		content := strings.TrimSpace(string(data))
		if content != "" {
			memories = append(memories, &store.Memory{
				Content: truncate(content, 5000),
				Type:    store.ShortTerm,
				Scope:   "global",
				Tags:    []string{"fingersaver", "chat"},
				Source:  "fingersaver",
			})
		}
	}

	return memories, nil
}
