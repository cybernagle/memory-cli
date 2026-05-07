package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type CarAgentAdapter struct {
	Path string
}

func (a *CarAgentAdapter) Name() string { return "car-agent" }

func (a *CarAgentAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".car-agent")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}
	var memories []*store.Memory

	if files, err := findFiles(a.Path, ".md"); err == nil {
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			memories = append(memories, &store.Memory{
				Content: truncate(content, 5000),
				Type:    store.LongTerm,
				Scope:   "global",
				Tags:    []string{"car-agent"},
				Source:  "car-agent",
			})
		}
	}

	return memories, nil
}
