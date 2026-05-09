package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type ClaudeAdapter struct {
	Path string
}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".claude")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}
	var memories []*store.Memory

	if files, err := findFiles(a.Path, "CLAUDE.md"); err == nil {
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			memories = append(memories, &store.Memory{
				Content:  content,
				Phase:    store.PhaseOrganized,
				Category: store.CategoryKnowledge,
				Scope:    "global",
				Tags:     []string{"claude", "instructions"},
				Source:   "claude",
			})
		}
	}

	projectsDir := filepath.Join(a.Path, "projects")
	if dirs, err := findDirs(projectsDir, "memory"); err == nil {
		for _, dir := range dirs {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				content := strings.TrimSpace(string(data))
				if content == "" {
					continue
				}
				memories = append(memories, &store.Memory{
					Content:  content,
					Phase:    store.PhaseOrganized,
					Category: store.CategoryKnowledge,
					Scope:    "global",
					Tags:     []string{"claude", "project-memory"},
					Source:   "claude",
				})
			}
		}
	}

	return memories, nil
}
