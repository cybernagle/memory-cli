package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/cybernagle/memory-cli/internal/store"
)

type MemorySmartSearchTool struct {
	store *store.Store
}

func (t *MemorySmartSearchTool) Name() string { return "memory_smart_search" }

func (t *MemorySmartSearchTool) Description() string {
	return "Intelligent memory search that tokenizes the query, matches by relevance score, and supports natural language queries."
}

func (t *MemorySmartSearchTool) Parameters() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]ToolProperty{
			"query":            {Type: "string", Description: "Natural language query (Chinese/English). Automatically tokenized for fuzzy matching."},
			"top":              {Type: "integer", Description: "Return top N results by relevance (default 10)"},
			"scope":            {Type: "string", Description: "Filter by scope"},
			"phase":            {Type: "string", Description: "Filter by phase: inbox or organized", Enum: []string{"inbox", "organized"}},
			"category":         {Type: "string", Description: "Filter by category"},
			"source":           {Type: "string", Description: "Filter by source"},
			"tags":             {Type: "string", Description: "Comma-separated tags (all must match)"},
			"created_after":    {Type: "string", Description: "ISO 8601 datetime, only memories created after this time"},
			"created_before":   {Type: "string", Description: "ISO 8601 datetime, only memories created before this time"},
			"include_related":  {Type: "boolean", Description: "Include memories linked to matched results (default false)"},
		},
		Required: []string{"query"},
	}
}

type scoredMemory struct {
	Memory *store.Memory
	Score  float64
	Tokens []string
}

type smartSearchResult struct {
	ID       string   `json:"id"`
	Content  string   `json:"content"`
	Phase    string   `json:"phase"`
	Category string   `json:"category"`
	Scope    string   `json:"scope"`
	Source   string   `json:"source"`
	Tags     []string `json:"tags"`
	Score    float64  `json:"score"`
	Matched  []string `json:"matched_tokens"`
	Preview  string   `json:"preview"`
}

func (t *MemorySmartSearchTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return ErrResult("query is required"), nil
	}

	top := 10
	if v, ok := params["top"].(float64); ok && v > 0 {
		top = int(v)
	}

	opts := store.ListOptions{}
	if v, ok := params["phase"].(string); ok && v != "" {
		opts.Phase = store.Phase(v)
	}
	if v, ok := params["category"].(string); ok && v != "" {
		opts.Category = store.Category(v)
	}
	if v, ok := params["scope"].(string); ok {
		opts.Scope = v
	}
	if v, ok := params["source"].(string); ok {
		opts.Source = v
	}
	if v, ok := params["tags"].(string); ok && v != "" {
		opts.Tags = strings.Split(v, ",")
	}
	if v, ok := params["created_after"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.CreatedAfter = &t
		}
	}
	if v, ok := params["created_before"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.CreatedBefore = &t
		}
	}

	all, err := t.store.List(opts)
	if err != nil {
		return ErrResult(fmt.Sprintf("list failed: %v", err)), err
	}

	tokens := tokenize(query)
	if len(tokens) == 0 {
		return ErrResult("no valid tokens in query"), nil
	}

	now := time.Now()
	var scored []scoredMemory
	for _, mem := range all {
		score, matched := scoreMemory(mem, tokens, now)
		if score > 0 {
			scored = append(scored, scoredMemory{Memory: mem, Score: score, Tokens: matched})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if top > len(scored) {
		top = len(scored)
	}

	results := make([]smartSearchResult, 0, top)
	matchedIDs := make(map[string]bool)
	for i := 0; i < top; i++ {
		mem := scored[i].Memory
		matchedIDs[mem.ID] = true
		results = append(results, smartSearchResult{
			ID:       mem.ID,
			Content:  mem.Content,
			Phase:    string(mem.Phase),
			Category: string(mem.Category),
			Scope:    mem.Scope,
			Source:   mem.Source,
			Tags:     mem.Tags,
			Score:    math.Round(scored[i].Score*100) / 100,
			Matched:  scored[i].Tokens,
			Preview:  truncateContent(mem.Content, 120),
		})
	}

	// Related expansion
	includeRelated := false
	if v, ok := params["include_related"].(bool); ok {
		includeRelated = v
	}

	var related []smartSearchResult
	if includeRelated {
		relatedIDs := make(map[string]bool)
		for id := range matchedIDs {
			relatedIDs[id] = true
		}
		for id := range matchedIDs {
			mem, err := t.store.FindByID(id)
			if err != nil {
				continue
			}
			for _, linkID := range mem.Links {
				if relatedIDs[linkID] {
					continue
				}
				linked, err := t.store.FindByID(linkID)
				if err != nil {
					continue
				}
				relatedIDs[linkID] = true
				related = append(related, smartSearchResult{
					ID:       linked.ID,
					Content:  linked.Content,
					Phase:    string(linked.Phase),
					Category: string(linked.Category),
					Scope:    linked.Scope,
					Source:   linked.Source,
					Tags:     linked.Tags,
					Score:    0,
					Matched:  []string{"related"},
					Preview:  truncateContent(linked.Content, 120),
				})
			}
		}
	}

	data, _ := json.Marshal(map[string]any{
		"query":        query,
		"tokens":       tokens,
		"total_scored": len(scored),
		"returned":     len(results),
		"results":      results,
		"related":      related,
	})
	return OkResult(string(data), map[string]any{"count": len(results)}), nil
}

func tokenize(query string) []string {
	var tokens []string
	var buf strings.Builder

	for _, r := range query {
		if unicode.Is(unicode.Han, r) {
			if buf.Len() > 0 {
				if w := strings.TrimSpace(buf.String()); w != "" {
					tokens = append(tokens, strings.ToLower(w))
				}
				buf.Reset()
			}
			tokens = append(tokens, string(r))
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf.WriteRune(r)
		} else {
			if buf.Len() > 0 {
				if w := strings.TrimSpace(buf.String()); w != "" {
					tokens = append(tokens, strings.ToLower(w))
				}
				buf.Reset()
			}
		}
	}
	if buf.Len() > 0 {
		if w := strings.TrimSpace(buf.String()); w != "" {
			tokens = append(tokens, strings.ToLower(w))
		}
	}

	return dedup(tokens)
}

func scoreMemory(mem *store.Memory, tokens []string, now time.Time) (float64, []string) {
	content := strings.ToLower(mem.Content)
	tagsLower := make(map[string]bool)
	for _, t := range mem.Tags {
		tagsLower[strings.ToLower(t)] = true
	}

	var score float64
	var matched []string
	matchedSet := make(map[string]bool)

	for _, token := range tokens {
		if matchedSet[token] {
			continue
		}

		count := strings.Count(content, token)
		if count > 0 {
			s := 1.0
			switch {
			case count >= 5:
				s = 3.0
			case count >= 3:
				s = 2.0
			case count >= 2:
				s = 1.5
			}
			if tagsLower[token] {
				s += 1.0
			}
			score += s
			matchedSet[token] = true
			matched = append(matched, token)
		}
	}

	if score == 0 {
		return 0, nil
	}

	// Recency boost: half-life of 7 days
	ageHours := now.Sub(mem.CreatedAt).Hours()
	recencyFactor := 1.0 / (1.0 + ageHours/168.0)
	score *= (1.0 + recencyFactor)

	// Access frequency boost: max 2x
	if mem.AccessCount > 0 {
		score *= (1.0 + math.Min(float64(mem.AccessCount)*0.1, 1.0))
	}

	return score, matched
}

func truncateContent(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func dedup(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
