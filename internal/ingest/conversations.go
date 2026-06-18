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
		// Prefer the on-disk filename as the initial session id, but per-entry sessionId inside
		// the file wins (subagent transcripts under subagents/ reuse the PARENT session id).
		defaultSessionID := strings.TrimSuffix(info.Name(), ".jsonl")
		isSubagentFile := strings.Contains(path, "subagents/")
		msgs := parseJSONL(path, project, defaultSessionID, isSubagentFile)
		memories = append(memories, msgs...)
		return nil
	})

	return memories, err
}

// jsonlEntry models the subset of Claude Code transcript fields we capture. The full schema
// has ~49 keys; we keep the load-bearing ones for memory provenance and context threading.
type jsonlEntry struct {
	Type             string `json:"type"`
	UUID             string `json:"uuid"`
	ParentUUID       string `json:"parentUuid"`
	SessionID        string `json:"sessionId"`
	Timestamp        string `json:"timestamp"`
	Cwd              string `json:"cwd"`
	GitBranch        string `json:"gitBranch"`
	Version          string `json:"version"`
	PromptID         string `json:"promptId"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	AgentID          string `json:"agentId"`
	Message          struct {
		Role    string      `json:"role"`
		Model   string      `json:"model"`
		Content interface{} `json:"content"`
	} `json:"message"`
}

func parseJSONL(path, project, defaultSessionID string, isSubagentFile bool) []*store.Memory {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var memories []*store.Memory
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 4*1024*1024)

	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		// Only user/assistant turns carry recallable content. Skip system/attachment/summary-meta/
		// file-history/etc. entries (they are transcript bookkeeping, not memory material).
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		// isMeta entries are injected context / local-command caveats — pure noise.
		if entry.IsMeta {
			continue
		}

		// Resolve session: the entry's own sessionId is authoritative. Subagent transcript rows
		// reuse the PARENT session id, so relying on it (instead of the filename) correctly
		// attributes subagent messages to their parent session.
		sessionID := entry.SessionID
		if sessionID == "" {
			sessionID = defaultSessionID
		}

		role := entry.Type
		if entry.Message.Role != "" {
			role = entry.Message.Role
		}

		// Extract textual content. For user turns this is the prompt; for assistant turns it
		// is the answer text plus any thinking blocks (high-value decision reasoning). Tool-use
		// and tool-result blocks are deliberately dropped — they're command/output noise.
		content := extractContent(entry.Message.Content, role)
		if content == "" || shouldFilterContent(content) {
			continue
		}

		// Derive the concrete project from the entry's exact cwd (not the lossy dir-name decode).
		proj := ProjectFromCwd(entry.Cwd)
		if proj == "" {
			proj = project // fallback to directory-derived name
		}

		var createdAt time.Time
		if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			createdAt = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", entry.Timestamp); err == nil {
			createdAt = t
		} else {
			createdAt = time.Now()
		}

		// Build provenance tags. "claude" + "conversation" are always present; the rest are
		// conditional markers that let downstream filtering distinguish compact summaries,
		// subagent turns, and thinking blocks at a glance.
		tags := []string{"claude", "conversation"}
		if entry.IsCompactSummary {
			tags = append(tags, "compact-summary")
		}
		if isSubagentFile || entry.IsSidechain {
			tags = append(tags, "subagent")
		}
		if entry.AgentID != "" {
			tags = append(tags, "agent:"+entry.AgentID)
		}

		memories = append(memories, &store.Memory{
			Content:     content,
			Phase:       store.PhaseInbox,
			Category:    store.CategoryKnowledge,
			Scope:       "global",
			Tags:        tags,
			Source:      "conversations",
			SessionID:   sessionID,
			Project:     proj,
			CreatedAt:   createdAt,
			MessageUUID: entry.UUID,
			ParentUUID:  entry.ParentUUID,
			Role:        role,
			GitBranch:   entry.GitBranch,
			Model:       entry.Message.Model,
			PromptID:    entry.PromptID,
		})
	}

	return memories
}

// extractContent pulls textual content out of a message. For assistant turns it concatenates
// thinking blocks (prefixed) with text blocks — both are recallable. For user turns it takes
// the text. Tool-use and tool-result blocks are skipped.
func extractContent(raw interface{}, role string) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			switch t {
			case "text":
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			case "thinking":
				// Only assistant turns carry thinking; keep it but mark it so the memory
				// preserves that this was reasoning, not a stated answer.
				if s, ok := m["thinking"].(string); ok && s != "" {
					parts = append(parts, "[thinking] "+s)
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

// shouldFilterContent returns true for conversational noise that should not become memories.
func shouldFilterContent(content string) bool {
	// Too short to contain meaningful information
	if len(content) < 10 {
		return true
	}

	lower := strings.ToLower(strings.TrimSpace(content))

	// Slash commands
	if strings.HasPrefix(lower, "/") {
		return true
	}

	// Shell commands
	if strings.HasPrefix(lower, "$ ") || strings.HasPrefix(lower, "> ") {
		return true
	}

	// HTML/bash tags (tool output artifacts)
	if strings.HasPrefix(lower, "<bash-input>") || strings.HasPrefix(lower, "<bash-output>") {
		return true
	}

	// Pure punctuation or digits
	allPunct := true
	for _, r := range lower {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= '一' && r <= '鿿' {
			allPunct = false
			break
		}
	}
	if allPunct {
		return true
	}

	// Common greetings/filler (exact or very short matches)
	fillers := []string{
		"hello", "hi", "hey", "ok", "okay", "thanks", "thank you",
		"yes", "no", "yep", "nope", "sure", "cool", "nice",
		"继续", "好的", "谢谢", "对", "嗯", "可以",
		"go ahead", "please", "continue", "skip", "next",
	}
	trimmed := strings.TrimSpace(lower)
	for _, f := range fillers {
		if trimmed == f {
			return true
		}
	}

	return false
}
