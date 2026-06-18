package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client talks to an OpenAI-compatible Chat Completions endpoint (default: z.ai GLM-4.5-Flash).
// It replaces the previous Anthropic-SDK-backed client. The public method surface
// (Extract/Merge/ConceptTags/Chat/ChatWithTokens) is unchanged so callers need no edits.
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string // e.g. https://api.z.ai/api/paas/v4
	model      string // e.g. glm-4.5-flash
}

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// Model returns the configured model name (for diagnostics/logging).
func (c *Client) Model() string { return c.model }

// BaseURL returns the configured API base URL (for diagnostics/logging).
func (c *Client) BaseURL() string { return c.baseURL }

// DefaultModel is the free z.ai Flash model used when no model is configured.
const DefaultModel = "glm-4.5-flash"

// DefaultBaseURL is the OpenAI-compatible Chat Completions endpoint. This is the Zhipu/z.ai
// domestic endpoint (verified working with glm-4.5-flash). Note: the api.z.ai and
// open.bigmodel.cn hosts are the same service; the latter is the domestic China endpoint.
const DefaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"

// isAnthropicProtocolBaseURL reports whether a base URL points at an Anthropic-Messages
// protocol adapter (e.g. ".../api/anthropic"). Such URLs speak a DIFFERENT request format
// than OpenAI Chat Completions, so they cannot be used by this client — the key is reusable,
// but the URL must fall back to the OpenAI-compatible default.
func isAnthropicProtocolBaseURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "/api/anthropic") || strings.HasSuffix(lower, "/anthropic")
}

// NewClient resolves credentials, reusing an existing z.ai/Zhipu key wherever it lives.
// The key is looked up from (in order): Config.APIKey, MEMORY_LLM_API_KEY, GLM_API_KEY,
// ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, then the same names in ~/.claude/settings.json.
//
// The base URL must be OpenAI-compatible (/paas/v4). If a discovered URL is an Anthropic
// protocol adapter (the old /api/anthropic path), it is IGNORED and the OpenAI default is
// used instead — the key still works, but the request format differs. Model defaults to
// glm-4.5-flash (free, no thinking) unless MEMORY_LLM_MODEL overrides it.
func NewClient(cfg Config) (*Client, error) {
	apiKey := cfg.APIKey
	baseURL := cfg.BaseURL
	model := cfg.Model

	// Resolve the API key: try new name, then the existing z.ai names, then legacy Anthropic names.
	// (The same z.ai/Zhipu key works across all of these — only the variable name differs by setup.)
	if apiKey == "" {
		apiKey = firstNonEmpty(
			os.Getenv("MEMORY_LLM_API_KEY"),
			os.Getenv("GLM_API_KEY"),
			os.Getenv("ANTHROPIC_API_KEY"),
		)
	}
	// Resolve base URL: MEMORY_LLM_BASE_URL wins; legacy ANTHROPIC_BASE_URL is only used if it
	// is NOT an Anthropic-protocol adapter (it often is — e.g. open.bigmodel.cn/api/anthropic —
	// and would break this client's OpenAI-format requests).
	if baseURL == "" {
		candidate := os.Getenv("MEMORY_LLM_BASE_URL")
		if candidate == "" {
			candidate = os.Getenv("ANTHROPIC_BASE_URL")
		}
		if candidate != "" && !isAnthropicProtocolBaseURL(candidate) {
			baseURL = candidate
		}
	}
	if model == "" {
		model = os.Getenv("MEMORY_LLM_MODEL")
	}

	// ~/.claude/settings.json fallback: many users already have a z.ai key configured there
	// (ANTHROPIC_AUTH_TOKEN / GLM_API_KEY) for Claude Code pointed at z.ai.
	if apiKey == "" {
		if settings, err := loadClaudeSettings(); err == nil {
			apiKey = firstNonEmpty(
				settings.Env["MEMORY_LLM_API_KEY"],
				settings.Env["GLM_API_KEY"],
				settings.Env["ANTHROPIC_AUTH_TOKEN"],
			)
			if baseURL == "" {
				candidate := settings.Env["MEMORY_LLM_BASE_URL"]
				if candidate == "" {
					candidate = settings.Env["ANTHROPIC_BASE_URL"]
				}
				if candidate != "" && !isAnthropicProtocolBaseURL(candidate) {
					baseURL = candidate
				}
			}
			if model == "" {
				model = settings.Env["MEMORY_LLM_MODEL"]
			}
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("no API key found: set GLM_API_KEY / MEMORY_LLM_API_KEY, or configure ~/.claude/settings.json")
	}

	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}

	return &Client{
		httpClient: newHTTPClient(),
		apiKey:     apiKey,
		baseURL:    baseURL,
		model:      model,
	}, nil
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	prompt := buildExtractPrompt(req)
	start := time.Now()
	result, err := c.chat(ctx, prompt, 4096, true) // json mode
	recordLLMCall("Extract", c.model, prompt, fmt.Sprintf("contents=%d", len(req.Contents)),
		result, err, time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(extractJSON(result.text)), &memories); err != nil {
		return nil, fmt.Errorf("parse llm response: %w\nraw: %s", err, result.text)
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
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	prompt := buildMergePrompt(req)
	start := time.Now()
	result, err := c.chat(ctx, prompt, 4096, true) // json mode
	recordLLMCall("Merge", c.model, prompt, fmt.Sprintf("memories=%d", len(req.Memories)),
		result, err, time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	jsonStr := repairJSON(extractJSON(result.text))
	// Parse into raw maps to handle model quirks (nested arrays, etc.)
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &rawItems); err != nil {
		return nil, fmt.Errorf("parse llm response: %w\nraw: %s", err, result.text[:min(500, len(result.text))])
	}
	var merged []MergedMemory
	for _, item := range rawItems {
		var m MergedMemory
		if v, ok := item["content"]; ok {
			json.Unmarshal(v, &m.Content)
		}
		if v, ok := item["categories"]; ok {
			m.Categories = parseStringArray(v)
		}
		if v, ok := item["tags"]; ok {
			m.Tags = parseStringArray(v)
		}
		if v, ok := item["confidence"]; ok {
			json.Unmarshal(v, &m.Confidence)
		}
		if v, ok := item["source_ids"]; ok {
			m.SourceIDs = parseStringArray(v)
		}
		merged = append(merged, m)
	}
	return merged, nil
}

// parseStringArray handles both ["a","b"] and [["a","b"]] formats from LLMs
func parseStringArray(raw json.RawMessage) []string {
	// Try normal array first
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	// Try nested array
	var nested [][]string
	if err := json.Unmarshal(raw, &nested); err == nil {
		var flat []string
		for _, inner := range nested {
			flat = append(flat, inner...)
		}
		return flat
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

	return fmt.Sprintf(`You are a memory extraction engine. Extract ONLY long-term knowledge from raw conversation logs.

Your job is to identify facts, preferences, decisions, and insights that remain valuable MONTHS from now.
Most inputs are conversational noise — you should DISCARD the vast majority.

EXTRACT (keep):
- User preferences, habits, or personal traits
- Technical decisions with rationale (why, not just what)
- Project goals, architecture choices, design decisions
- Lessons learned, bugs solved with root cause
- Contact info, credentials, account details
- Recurring patterns or workflows

DISCARD (reject):
- Daily activities (buying items, checking battery, weather)
- Greetings, acknowledgments, small talk
- Debugging logs, error output, stack traces
- Questions without definitive answers
- Session management (open/close/switch)
- System prompts, agent instructions, persona descriptions
- Temporary state (current time, current task, today's plan)
- Raw timestamps or date-log entries (e.g. "- 08:33 found device")
- Anything you wouldn't bother writing in a personal notebook

Rules:
- Use [[wiki-links]] for key concepts: "User prefers [[Go]] over [[Python]]"
- Each memory = ONE concise sentence, no markdown headers, no timestamps
- Merge similar items aggressively. Output MUST be fewer than inputs.
- Tags: lowercase, short, specific
- Confidence: 0.0-1.0 — only 0.8+ for verified facts
- If nothing is worth remembering, return []

Available catalogs (extend with [[new]]): %s

Raw content:%s

JSON array only:
[{"content":"...","categories":["[[concept1]]"],"tags":["tag1"],"confidence":0.9}]
If nothing qualifies, respond: []`, catalogs, contentList)
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

	return fmt.Sprintf(`You are a memory consolidation engine. AGGRESSIVELY merge related memories.

Your goal: produce the MINIMUM number of high-quality memories. Every output should be a dense, information-rich paragraph.

MERGE RULES (strict):
- Group memories by topic/project/concept FIRST
- ALL memories about the same project/topic MUST become ONE merged memory
- Example: 5 memories about [[car-agent]] -> 1 comprehensive car-agent summary
- Example: 3 memories about user preferences -> 1 preference summary
- The merged result must be RICHER than any individual input
- Keep ALL [[wiki-links]] from source memories
- You MUST produce at MOST 50%% of the input count (ideally 30%%)
- Only keep unrelated memories separate if they cover genuinely different topics
- Each source ID maps to its merged result

Input memories:%s

Respond with a JSON array only:
[{"content":"...","categories":["[[concept]]"],"tags":["t1"],"confidence":0.95,"source_ids":["1","3"]}]`, list)
}

// ConceptTagsResult is the per-input tag set returned by ConceptTags.
type ConceptTagsResult struct {
	Tags []string `json:"tags"`
}

// ConceptTags asks the model for semantic concept/topic tags for each content string.
// Unlike the free keyword extractor, this captures abstract topics and works in the content's
// own language (including Chinese). Output order matches the input order.
func (c *Client) ConceptTags(ctx context.Context, contents []string) ([][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	prompt := buildConceptTagsPrompt(contents)
	start := time.Now()
	result, err := c.chat(ctx, prompt, 4096, true) // json mode
	recordLLMCall("ConceptTags", c.model, prompt, fmt.Sprintf("contents=%d", len(contents)),
		result, err, time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	var results []ConceptTagsResult
	if err := json.Unmarshal([]byte(extractJSON(result.text)), &results); err != nil {
		return nil, fmt.Errorf("parse llm response: %w\nraw: %s", err, result.text)
	}

	out := make([][]string, len(contents))
	for i := 0; i < len(results) && i < len(contents); i++ {
		out[i] = results[i].Tags
	}
	return out, nil
}

func buildConceptTagsPrompt(contents []string) string {
	list := ""
	for i, c := range contents {
		list += fmt.Sprintf("\n%d. %s", i+1, c)
	}
	return fmt.Sprintf(`You are a semantic tagging engine. For each memory below, output 3-6 tags that capture WHAT it is about — its topics, concepts, technologies, and domain.

Rules:
- Tag the SUBJECT MATTER, not the provenance. Capture what the memory discusses.
- Use the SAME LANGUAGE as the content (Chinese content → Chinese tags; English → English). Mixed content → mixed tags are fine.
- Be specific and concrete: "状态管理", "react-hooks", "印章验真", "goroutine-泄漏", not generic words like "记忆"/"信息"/"code".
- Each tag lowercase, hyphen-separated if multi-word.
- Do NOT output provenance/source tags: no "claude-session", "cat:knowledge", "consolidated", project names, or timestamps.
- Output exactly one tags-array per input, in input order.

Memories:%s

Respond with a JSON array only, one entry per input:
[{"tags":["tag1","tag2"]}]
If a memory has no extractable subject matter, respond with an empty array for it: {"tags":[]}`, list)
}

func (c *Client) Chat(ctx context.Context, prompt string) (string, error) {
	return c.ChatWithTokens(ctx, prompt, 1024)
}

func (c *Client) ChatWithTokens(ctx context.Context, prompt string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	start := time.Now()
	result, err := c.chat(ctx, prompt, maxTokens, false) // plain text
	recordLLMCall("Chat", c.model, prompt, fmt.Sprintf("maxTokens=%d", maxTokens),
		result, err, time.Since(start))
	if err != nil {
		return "", fmt.Errorf("llm chat: %w", err)
	}
	return result.text, nil
}

// extractJSON locates the first balanced JSON array/object in s. Kept as a safety net even
// though json_object mode normally returns clean JSON — some gateways wrap the payload in
// prose or code fences.
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

// repairJSON attempts to fix common LLM JSON errors
func repairJSON(s string) string {
	// Strip markdown code fences if present
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// Remove trailing commas before } or ]
	for {
		old := s
		s = strings.Replace(s, ",]", "]", -1)
		s = strings.Replace(s, ",}", "}", -1)
		if s == old {
			break
		}
	}
	return s
}
