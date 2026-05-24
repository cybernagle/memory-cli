package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type Client struct {
	client *anthropic.Client
	model  string
}

type Config struct {
	APIKey string
	Model  string
}

func NewClient(cfg Config) (*Client, error) {
	apiKey := cfg.APIKey
	baseURL := ""
	model := cfg.Model

	// 1. Try config params
	if apiKey == "" {
		// 2. Try env vars
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
		if baseURL == "" {
			baseURL = os.Getenv("ANTHROPIC_BASE_URL")
		}
		if model == "" {
			model = os.Getenv("ANTHROPIC_DEFAULT_SONNET_MODEL")
		}
	}

	// 3. Try ~/.claude/settings.json
	if apiKey == "" {
		if settings, err := loadClaudeSettings(); err == nil {
			if v, ok := settings.Env["ANTHROPIC_AUTH_TOKEN"]; ok && v != "" {
				apiKey = v
			}
			if v, ok := settings.Env["ANTHROPIC_BASE_URL"]; ok && v != "" {
				baseURL = v
			}
			if model == "" {
				if v, ok := settings.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; ok && v != "" {
					model = v
				}
			}
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("no API key found: set ANTHROPIC_API_KEY or configure ~/.claude/settings.json")
	}

	if model == "" {
		model = string(anthropic.ModelClaudeSonnet4_20250514)
	}

	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	client := anthropic.NewClient(opts...)
	return &Client{client: &client, model: model}, nil
}

type claudeSettings struct {
	Env map[string]string `json:"env"`
}

func loadClaudeSettings() (*claudeSettings, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

type ExtractRequest struct {
	Contents []string
	Catalogs []string
}

type ExtractedMemory struct {
	Content    string   `json:"content"`
	Categories []string `json:"categories"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
}

func (c *Client) Extract(ctx context.Context, req ExtractRequest) ([]ExtractedMemory, error) {
	prompt := buildExtractPrompt(req)
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		MaxTokens: 4096,
		Model:     c.model,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	text := ""
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(extractJSON(text)), &memories); err != nil {
		return nil, fmt.Errorf("parse llm response: %w\nraw: %s", err, text)
	}
	return memories, nil
}

type MergeRequest struct {
	Memories []MergedMemory
}

type MergedMemory struct {
	Content    string   `json:"content"`
	Categories []string `json:"categories"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
	SourceIDs  []string `json:"source_ids"`
}

func (c *Client) Merge(ctx context.Context, req MergeRequest) ([]MergedMemory, error) {
	prompt := buildMergePrompt(req)
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		MaxTokens: 4096,
		Model:     c.model,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	text := ""
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	var merged []MergedMemory
	if err := json.Unmarshal([]byte(extractJSON(text)), &merged); err != nil {
		return nil, fmt.Errorf("parse llm response: %w\nraw: %s", err, text)
	}
	return merged, nil
}

func buildExtractPrompt(req ExtractRequest) string {
	catalogs := "general knowledge"
	if len(req.Catalogs) > 0 {
		catalogs = fmt.Sprintf("%v", req.Catalogs)
	}

	contentList := ""
	for i, c := range req.Contents {
		contentList += fmt.Sprintf("\n%d. %s", i+1, c)
	}

	return fmt.Sprintf(`You are a memory extraction engine. Extract structured long-term memories from the raw content below.

Rules:
- Extract ONLY facts, preferences, decisions, insights, and lessons. No filler.
- Use [[wiki-links]] to tag key concepts, e.g. "User prefers [[Go]] over [[Python]]"
- Each extracted memory must be a single concise sentence
- You MUST produce FEWER memories than raw inputs. Merge similar items.
- Tags should be lowercase, short, specific
- Confidence: 0.0-1.0 how certain this is a lasting insight
- DISCARD messages that are NOT lasting knowledge: session management, UI commands, greetings, acknowledgments, simple calculations, questions without answers, debugging noise, task instructions (e.g. "open dashboard", "run tests", "rename session"). Only keep information worth remembering weeks later.

Available concept catalogs (auto-extend with [[new]] as needed): %s

Raw content:%s

Respond with a JSON array only, no other text:
[{"content":"...","categories":["[[concept1]]"],"tags":["tag1","tag2"],"confidence":0.9}]`, catalogs, contentList)
}

func buildMergePrompt(req MergeRequest) string {
	list := ""
	for i, m := range req.Memories {
		tags := ""
		if len(m.Tags) > 0 {
			tags = fmt.Sprintf(" [tags: %v]", m.Tags)
		}
		list += fmt.Sprintf("\n%d. %s%s (confidence: %.1f)", i+1, m.Content, tags, m.Confidence)
	}

	return fmt.Sprintf(`You are a memory consolidation engine. Merge related memories into fewer, denser memories.

Rules:
- Merge memories about the same topic into one
- The merged memory should be richer and more precise than any individual input
- Keep all [[wiki-links]] from source memories
- You MUST produce FEWER outputs than inputs
- If memories are unrelated, keep them separate
- Each source memory ID maps to the merged result

Input memories:%s

Respond with a JSON array only:
[{"content":"...","categories":["[[concept]]"],"tags":["t1"],"confidence":0.95,"source_ids":["1","3"]}]`, list)
}

func extractJSON(s string) string {
	start := -1
	end := -1
	depth := 0
	for i, c := range s {
		if c == '[' {
			if depth == 0 {
				start = i
			}
			depth++
		}
		if c == ']' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}
	if start >= 0 && end > start {
		return s[start:end]
	}
	return "[]"
}
