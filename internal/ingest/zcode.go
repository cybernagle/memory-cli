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

// ZcodeAdapter ingests zcode conversation rollouts from ~/.zcode/cli/rollout/.
// Unlike Claude's JSONL transcripts, zcode logs each API call as a line containing the
// request (with a sliding message window) and the response. We extract one memory per turn:
// the assistant's text response paired with the user prompt that triggered it. Subagent
// rollouts (sess_subagent_*) are included but tagged.
type ZcodeAdapter struct {
	Path string
}

func (a *ZcodeAdapter) Name() string { return "zcode" }

func (a *ZcodeAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".zcode", "cli", "rollout")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}

	var memories []*store.Memory

	err := filepath.Walk(a.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}

		fname := info.Name()
		isSubagent := strings.Contains(fname, "subagent")
		// Session id is embedded in the filename: model-io-sess_<id>.jsonl
		sessionID := strings.TrimPrefix(strings.TrimSuffix(fname, ".jsonl"), "model-io-sess_")

		msgs := parseZcodeRollout(path, sessionID, isSubagent)
		memories = append(memories, msgs...)
		return nil
	})

	return memories, err
}

// zcodeTurn is one API call line in the rollout JSONL.
type zcodeTurn struct {
	RequestID  string `json:"requestId"`
	SessionID  string `json:"sessionId"`
	Model      struct {
		ModelID    string `json:"modelId"`
		ProviderID string `json:"providerId"`
	} `json:"model"`
	StartedAt string `json:"startedAt"`
	Type      string `json:"type"`
	Request   struct {
		Messages []zcodeMessage `json:"messages"`
	} `json:"request"`
	Response struct {
		Text       string `json:"text"`
		ToolCalls  []json.RawMessage `json:"toolCalls"`
		FinishReason string `json:"finishReason"`
	} `json:"response"`
}

// zcodeMessage is a single message in the request's sliding window.
type zcodeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

func parseZcodeRollout(path, sessionID string, isSubagent bool) []*store.Memory {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var memories []*store.Memory
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 8*1024*1024) // rollout lines can be large (full message windows)

	for scanner.Scan() {
		var turn zcodeTurn
		if err := json.Unmarshal(scanner.Bytes(), &turn); err != nil {
			continue
		}
		if turn.Type != "model_io" {
			continue
		}

		// Extract the assistant's textual response. Skip turns where the model only made tool
		// calls with no accompanying text — those are plumbing, not recallable content.
		assistantText := strings.TrimSpace(turn.Response.Text)
		if assistantText == "" || len(assistantText) < 20 {
			continue
		}

		// Find the user prompt that triggered this turn (last user message in the window).
		// This pairs the question with the answer for coherent extraction downstream.
		userPrompt := lastUserPrompt(turn.Request.Messages)
		if userPrompt != "" && shouldFilterContent(userPrompt) {
			userPrompt = "" // noise prompt (greeting/command); keep the answer but drop the prompt
		}

		// Build the memory content: if we have the user prompt, prefix it for context.
		content := assistantText
		if userPrompt != "" {
			content = "Q: " + userPrompt + "\nA: " + assistantText
		}

		if shouldFilterContent(content) {
			continue
		}

		var createdAt time.Time
		if t, err := time.Parse(time.RFC3339Nano, turn.StartedAt); err == nil {
			createdAt = t
		} else {
			createdAt = time.Now()
		}

		model := turn.Model.ModelID
		if model == "" {
			model = "zcode"
		}

		tags := []string{"zcode", "conversation"}
		if isSubagent {
			tags = append(tags, "subagent")
		}

		memories = append(memories, &store.Memory{
			Content:   content,
			Phase:     store.PhaseInbox,
			Category:  store.CategorizeContent(content),
			Scope:     "global",
			Tags:      tags,
			Source:    "zcode",
			SessionID: sessionID,
			Project:   "zcode", // zcode sessions don't carry a cwd; tag them uniformly
			CreatedAt: createdAt,
			Role:      "assistant",
			Model:     model,
		})
	}

	return memories
}

// lastUserPrompt scans the message window (newest first) for the most recent user message
// with textual content. Returns "" if none found.
func lastUserPrompt(msgs []zcodeMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "user" {
			continue
		}
		switch c := m.Content.(type) {
		case string:
			text := strings.TrimSpace(c)
			// Skip system-reminder injections and tool-result wrappers.
			if text != "" && !strings.HasPrefix(text, "<system-reminder>") {
				return text
			}
		case []interface{}:
			for _, item := range c {
				b, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if b["type"] == "text" {
					if s, ok := b["text"].(string); ok {
						s = strings.TrimSpace(s)
						if s != "" && !strings.HasPrefix(s, "<system-reminder>") {
							return s
						}
					}
				}
			}
		}
	}
	return ""
}
