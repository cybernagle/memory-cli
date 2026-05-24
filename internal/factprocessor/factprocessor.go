package factprocessor

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/store"
)

const (
	defaultBatchSize = 50
)

// FactProcessor implements plugin.Processor for fact extraction.
type FactProcessor struct {
	llm       *llm.Client
	resolver  *entity.EntityResolver
	batchSize int
}

func New(llmClient *llm.Client, entityComp *entity.EntityComponent) *FactProcessor {
	return &FactProcessor{
		llm:       llmClient,
		resolver:  entity.NewResolver(entityComp),
		batchSize: defaultBatchSize,
	}
}

func (p *FactProcessor) Name() string {
	return "fact-processor"
}

func (p *FactProcessor) Consumes() []plugin.DataType {
	return []plugin.DataType{plugin.DataEntity}
}

func (p *FactProcessor) Produces() []plugin.DataType {
	return []plugin.DataType{plugin.DataEntity}
}

const (
	maxContentChars   = 30000 // max total chars per LLM call
	maxContentsPerCall = 20   // max items per LLM call
)

func (p *FactProcessor) Process(ctx context.Context, input plugin.ProcessInput) (*plugin.ProcessOutput, error) {
	groups := groupBySession(input.Items)
	if len(groups) == 0 {
		return &plugin.ProcessOutput{}, nil
	}

	result := &plugin.ProcessOutput{}
	var allExtracted []llm.ExtractedMemory
	var allSourceIDs []string

	log.Printf("[fact-processor] %d sessions to process", len(groups))

	for sid, items := range groups {
		var contents []string
		for _, item := range items {
			contents = append(contents, item.Content)
			allSourceIDs = append(allSourceIDs, item.ID)
		}

		// Split large sessions into chunks respecting char and count limits
		chunks := chunkContents(contents, maxContentsPerCall, maxContentChars)
		log.Printf("[fact-processor] session %s: %d items → %d chunks", sid, len(contents), len(chunks))
		for ci, chunk := range chunks {
			log.Printf("[fact-processor] extracting chunk %d/%d (session %s, %d items)...", ci+1, len(chunks), sid, len(chunk))
			extracted, err := p.extract(ctx, chunk)
			if err != nil {
				log.Printf("[fact-processor] extract session %s chunk %d: %v", sid, ci, err)
				result.Errors++
				continue
			}
			log.Printf("[fact-processor] extracted %d memories from chunk %d", len(extracted), ci+1)

			if len(extracted) > len(chunk) {
				trimTo := len(chunk) - 1
				if trimTo < 1 {
					trimTo = 1
				}
				extracted = extracted[:trimTo]
			}

			allExtracted = append(allExtracted, extracted...)
		}
	}

	// Merge layer
	if len(allExtracted) > 0 {
		merged, err := p.merge(ctx, allExtracted)
		if err != nil {
			return nil, fmt.Errorf("merge: %w", err)
		}
		if len(merged) > len(allExtracted) {
			merged = merged[:len(allExtracted)]
		}

		for _, m := range merged {
			categories := extractCategories(m.Content)
			cat := "knowledge"
			if len(categories) > 0 {
				cat = categories[0]
			}
			tags := m.Tags
			if len(tags) == 0 {
				tags = m.Categories
			}

			// Resolve [[wiki-links]] into entities
			if p.resolver != nil {
				p.resolver.ResolveMentions(ctx, m.Content, "")
			}

			result.Results = append(result.Results, plugin.DataItem{
				DataType:  plugin.DataEntity,
				Operation: "create",
				Data: map[string]interface{}{
					"content":    m.Content,
					"category":   cat,
					"tags":       tags,
					"confidence": m.Confidence,
					"created_at": time.Now().Format(time.RFC3339),
				},
				Confidence: m.Confidence,
			})
		}
	}

	result.SourceIDs = allSourceIDs
	return result, nil
}

func (p *FactProcessor) extract(ctx context.Context, contents []string) ([]llm.ExtractedMemory, error) {
	return p.llm.Extract(ctx, llm.ExtractRequest{Contents: contents})
}

// chunkContents splits contents into chunks that fit within count and char limits.
func chunkContents(contents []string, maxCount, maxChars int) [][]string {
	var chunks [][]string
	var current []string
	chars := 0
	for _, c := range contents {
		if len(current) >= maxCount || (chars+len(c) > maxChars && len(current) > 0) {
			chunks = append(chunks, current)
			current = nil
			chars = 0
		}
		current = append(current, c)
		chars += len(c)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func (p *FactProcessor) merge(ctx context.Context, extracted []llm.ExtractedMemory) ([]llm.MergedMemory, error) {
	asMerged := make([]llm.MergedMemory, len(extracted))
	for i, e := range extracted {
		asMerged[i] = llm.MergedMemory{
			Content:    e.Content,
			Categories: e.Categories,
			Tags:       e.Tags,
			Confidence: e.Confidence,
		}
	}

	if len(asMerged) <= defaultBatchSize {
		return p.llm.Merge(ctx, llm.MergeRequest{Memories: asMerged})
	}

	var result []llm.MergedMemory
	for i := 0; i < len(asMerged); i += defaultBatchSize {
		end := i + defaultBatchSize
		if end > len(asMerged) {
			end = len(asMerged)
		}
		merged, err := p.llm.Merge(ctx, llm.MergeRequest{Memories: asMerged[i:end]})
		if err != nil {
			return nil, err
		}
		result = append(result, merged...)
	}

	if len(result) > defaultBatchSize {
		merged, err := p.llm.Merge(ctx, llm.MergeRequest{Memories: result})
		if err != nil {
			return nil, err
		}
		result = merged
	}

	return result, nil
}

func groupBySession(items []plugin.InboxItem) map[string][]plugin.InboxItem {
	groups := make(map[string][]plugin.InboxItem)
	var withSession, withoutSession []plugin.InboxItem
	for _, item := range items {
		if item.SessionID != "" {
			withSession = append(withSession, item)
		} else {
			withoutSession = append(withoutSession, item)
		}
	}
	for _, item := range withSession {
		groups[item.SessionID] = append(groups[item.SessionID], item)
	}
	if len(withoutSession) > 0 {
		sort.Slice(withoutSession, func(i, j int) bool {
			return withoutSession[i].CreatedAt.Before(withoutSession[j].CreatedAt)
		})
		groupID := fmt.Sprintf("time-%s", withoutSession[0].CreatedAt.Format("20060102-150405"))
		groups[groupID] = append(groups[groupID], withoutSession[0])
		for i := 1; i < len(withoutSession); i++ {
			prev := withoutSession[i-1]
			curr := withoutSession[i]
			if curr.CreatedAt.Sub(prev.CreatedAt) > 30*time.Minute {
				groupID = fmt.Sprintf("time-%s", curr.CreatedAt.Format("20060102-150405"))
			}
			groups[groupID] = append(groups[groupID], curr)
		}
	}
	return groups
}

func extractCategories(content string) []string {
	links := store.ExtractWikiLinks(content)
	var cats []string
	seen := make(map[string]bool)
	for _, l := range links {
		lower := strings.ToLower(l)
		if !seen[lower] {
			seen[lower] = true
			cats = append(cats, lower)
		}
	}
	return cats
}

// NewMemoryFromDataItem creates a store.Memory from a DataItem.
func NewMemoryFromDataItem(item plugin.DataItem) *store.Memory {
	content, _ := item.Data["content"].(string)
	cat, _ := item.Data["category"].(string)
	tagsRaw, _ := item.Data["tags"].([]string)
	confidence, _ := item.Data["confidence"].(float64)
	createdAt, _ := item.Data["created_at"].(string)

	if createdAt == "" {
		createdAt = time.Now().Format(time.RFC3339)
	}
	t, _ := time.Parse(time.RFC3339, createdAt)

	_ = confidence

	return &store.Memory{
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: store.HashContent(content),
		Phase:       store.PhaseOrganized,
		Category:    store.Category(cat),
		Scope:       "global",
		Tags:        tagsRaw,
		Source:      "fact-processor",
		CreatedAt:   t,
		UpdatedAt:   t,
		Version:     1,
	}
}
