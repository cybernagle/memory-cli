package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openaiChatRequest is the OpenAI-compatible request body posted to {baseURL}/chat/completions.
// Used for the z.ai/Zhipu GLM endpoint (open.bigmodel.cn/api/paas/v4), which implements the
// OpenAI Chat Completions protocol with a few GLM-specific extensions (notably `thinking`).
type openaiChatRequest struct {
	Model          string            `json:"model"`
	Messages       []openaiMessage   `json:"messages"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	ResponseFormat *openaiRespFormat `json:"response_format,omitempty"`
	// Thinking disables the model's reasoning mode. glm-4.5-flash defaults to thinking-on,
	// which burns output tokens on reasoning_content and is undesirable for these simple
	// summarize/extract tasks. Zhipu's extension field: {"type":"disabled"} turns it off.
	Thinking *glmThinking `json:"thinking,omitempty"`
}

// glmThinking is the Zhipu thinking-mode control. type="disabled" turns reasoning off so the
// model returns only the direct answer — what we want for cheap batch extraction/merge.
type glmThinking struct {
	Type string `json:"type"` // "enabled" | "disabled"
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiRespFormat asks the model for structured output. z.ai honors the OpenAI
// "json_object" mode, which is far more reliable than the old bracket-matching JSON extractor.
type openaiRespFormat struct {
	Type string `json:"type"` // "json_object" or "text"
}

type openaiChatResponse struct {
	Choices []struct {
		Message      openaiMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
	Error *openaiAPIError `json:"error,omitempty"`
}

// openaiAPIError is the {error: {message, type, code}} envelope returned on failures.
type openaiAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// chatResult is what the internal chat() helper returns to the public methods.
type chatResult struct {
	text             string
	promptTokens     int64
	completionTokens int64
}

// chat posts a single user prompt to the chat-completions endpoint and returns the text +
// token accounting. When jsonMode is true, the request carries response_format json_object
// so callers (Extract/Merge/ConceptTags) can json.Unmarshal the output directly.
func (c *Client) chat(ctx context.Context, prompt string, maxTokens int, jsonMode bool) (*chatResult, error) {
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	reqBody := openaiChatRequest{
		Model: c.model,
		Messages: []openaiMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
		// Disable reasoning: these are simple summarize/extract tasks; thinking wastes tokens
		// and latency. This is a no-op on endpoints that don't recognize the field.
		Thinking: &glmThinking{Type: "disabled"},
	}
	if jsonMode {
		reqBody.ResponseFormat = &openaiRespFormat{Type: "json_object"}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	// Some OpenAI-compatible gateways accept either Authorization or the x-api-key header;
	// set both so this works against z.ai and common proxies.
	httpReq.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Try to extract a structured error message; fall back to the raw body.
		var errResp openaiChatResponse
		msg := string(raw)
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error != nil && errResp.Error.Message != "" {
			msg = errResp.Error.Message
		}
		return nil, fmt.Errorf("llm http %d: %s", resp.StatusCode, truncateForError(msg))
	}

	var chatResp openaiChatResponse
	if err := json.Unmarshal(raw, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w\nraw: %s", err, truncateForError(string(raw)))
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}

	return &chatResult{
		text:             chatResp.Choices[0].Message.Content,
		promptTokens:     chatResp.Usage.PromptTokens,
		completionTokens: chatResp.Usage.CompletionTokens,
	}, nil
}

func truncateForError(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// newHTTPClient builds the *http.Client with sensible defaults used by the LLM client.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
	}
}
