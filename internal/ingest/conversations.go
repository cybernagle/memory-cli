package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type ConversationsAdapter struct {
	Path string
}

func (a *ConversationsAdapter) Name() string { return "conversations" }

func (a *ConversationsAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".claude", "projects")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}

	var memories []*store.Memory

	err := filepath.Walk(a.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}

		project := extractProjectName(filepath.Dir(path))
		sessionID := strings.TrimSuffix(info.Name(), ".jsonl")
		msgs := parseJSONL(path, project, sessionID)
		memories = append(memories, msgs...)
		return nil
	})

	return memories, err
}

type jsonlEntry struct {
	Type      string `json:"type"`
	UserType  string `json:"userType"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Role    string `json:"role"`
		Content interface{} `json:"content"`
	} `json:"message"`
}

func parseJSONL(path, project, sessionID string) []*store.Memory {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var memories []*store.Memory
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 2*1024*1024)

	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		if entry.Type != "user" || entry.UserType != "external" {
			continue
		}

		content := extractContent(entry.Message.Content)
		if content == "" {
			continue
		}

		var createdAt time.Time
		if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			createdAt = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", entry.Timestamp); err == nil {
			createdAt = t
		} else {
			createdAt = time.Now()
		}

		memories = append(memories, &store.Memory{
			Content:   content,
			Phase:     store.PhaseInbox,
			Category:  store.CategoryKnowledge,
			Scope:     "global",
			Tags:      []string{"claude", "conversation", project},
			Source:    "conversations",
			SessionID: sessionID,
			CreatedAt: createdAt,
		})
	}

	return memories
}

func extractContent(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if s, ok := m["text"].(string); ok {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func extractProjectName(dir string) string {
	base := filepath.Base(dir)
	base = strings.TrimPrefix(base, "-Users-naglezhang-Desktop-Code-")
	base = strings.TrimPrefix(base, "-Users-naglezhang-")
	base = strings.TrimPrefix(base, "-")
	base = strings.ReplaceAll(base, "-", "/")
	return base
}
