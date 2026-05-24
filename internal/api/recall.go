package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
)

type RecallRequest struct {
	Query     string `json:"query"`
	MaxTokens int    `json:"max_tokens"`
	Scope     string `json:"scope"`
}

type RecallResponse struct {
	Memories   []RecallItem `json:"memories"`
	TokensUsed int          `json:"tokens_used"`
	Truncated  bool         `json:"truncated"`
}

type RecallItem struct {
	ID       string  `json:"id"`
	Content  string  `json:"content"`
	Category string  `json:"category"`
	Score    float64 `json:"score"`
}

func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RecallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 2000
	}

	results, err := s.store.Search(store.SearchOptions{
		Query: req.Query,
		Scope: req.Scope,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var items []RecallItem
	tokensUsed := 0
	truncated := false

	for _, mem := range results {
		estimatedTokens := len(mem.Content) / 4
		if estimatedTokens == 0 {
			estimatedTokens = 1
		}
		if tokensUsed+estimatedTokens > req.MaxTokens {
			truncated = true
			break
		}

		score := float64(mem.AccessCount+1) / (1.0 + float64(strings.Count(mem.Content, " "))/10.0)
		items = append(items, RecallItem{
			ID:       mem.ID,
			Content:  mem.Content,
			Category: string(mem.Category),
			Score:    score,
		})
		tokensUsed += estimatedTokens
	}

	if items == nil {
		items = []RecallItem{}
	}

	logActivity(s, "recall", "", "http", req.Query)
	writeJSON(w, http.StatusOK, RecallResponse{
		Memories:   items,
		TokensUsed: tokensUsed,
		Truncated:  truncated,
	})
}
