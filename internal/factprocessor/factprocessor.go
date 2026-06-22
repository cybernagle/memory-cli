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
	maxContentChars    = 30000 // max total chars per LLM call
	maxContentsPerCall = 20    // max items per LLM call
	maxSessionsPerTick = 10    // max sessions processed per Process() call
	maxChunksPerTick   = 20    // max total LLM calls per tick
)

type FactProcessor struct {
	llm       *llm.Client
	resolver  *entity.EntityResolver
	batchSize int
}

func New(llmClient *llm.Client, entityComp *entity.EntityComponent) *FactProcessor {
	return &FactProcessor{
		llm:       llmClient,
		resolver:  entity.NewResolver(entityComp),
		batchSize: 50,
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

func (p *FactProcessor) Process(ctx context.Context, input plugin.ProcessInput) (*plugin.ProcessOutput, error) {
	groups := groupBySession(input.Items)
	if len(groups) == 0 {
		return &plugin.ProcessOutput{}, nil
	}

	result := &plugin.ProcessOutput{}
	var allSourceIDs []string

	log.Printf("[fact-processor] %d sessions to process (max %d sessions, %d chunks this tick)", len(groups), maxSessionsPerTick, maxChunksPerTick)

	// Process limited sessions per tick for incremental progress
	sessionCount := 0
	chunkCount := 0
	for sid, items := range groups {
		if sessionCount >= maxSessionsPerTick || chunkCount >= maxChunksPerTick {
			break
		}
		sessionCount++
		var contents []string
		var projects []string
		for _, item := range items {
			contents = append(contents, item.Content)
			projects = append(projects, item.Project)
			allSourceIDs = append(allSourceIDs, item.ID)
		}
		// Fallback project for the session — used for chunks whose items don't all agree on one
		// project (e.g. a cd mid-session or a subagent in a different cwd). Per-chunk attribution
		// is preferred; see chunkProject.
		sessionProject := ""
		if len(items) > 0 {
			sessionProject = items[0].Project
		}

		chunks, chunkProj := chunkContents(contents, projects, maxContentsPerCall, maxContentChars)
		log.Printf("[fact-processor] session %s: %d items → %d chunks", sid, len(contents), len(chunks))

		// Limit chunks from this session to stay within budget
		remaining := maxChunksPerTick - chunkCount
		if remaining <= 0 {
			break
		}
		if len(chunks) > remaining {
			chunks = chunks[:remaining]
			chunkProj = chunkProj[:remaining]
			log.Printf("[fact-processor] trimming session %s to %d chunks (budget)", sid, remaining)
		}

		for ci, chunk := range chunks {
			log.Printf("[fact-processor] extracting chunk %d/%d (session %s, %d items)...", ci+1, len(chunks), sid, len(chunk))
			extracted, err := p.extract(ctx, chunk)
			if err != nil {
				log.Printf("[fact-processor] extract session %s chunk %d: %v", sid, ci, err)
				result.Errors++
				continue
			}
			log.Printf("[fact-processor] extracted %d memories from chunk %d", len(extracted), ci+1)
			chunkCount++

			if len(extracted) > len(chunk) {
				trimTo := len(chunk) - 1
				if trimTo < 1 {
					trimTo = 1
				}
				extracted = extracted[:trimTo]
			}

			// Per-chunk project: if every item in this chunk shares one project, stamp it;
			// otherwise fall back to the session's first-item project. This fixes the bug where
			// a session spanning multiple projects (cd mid-session, foreign-cwd subagent) had its
			// minority-project memories mis-stamped with the first project.
			thisChunkProject := chunkProject(chunkProj[ci], sessionProject)

			for _, m := range extracted {
				cat := "knowledge"
				if len(m.Categories) > 0 {
					cat = m.Categories[0]
				} else if categories := extractCategories(m.Content); len(categories) > 0 {
					cat = categories[0]
				}
				tags := m.Tags
				if len(tags) == 0 {
					tags = m.Categories
				}

				// Generate the memory ID now so entity mentions are linked to the right memory.
				// The same id is threaded through the DataItem so NewMemoryFromDataItem reuses it.
				memID := uuid.New().String()
				if p.resolver != nil {
					p.resolver.ResolveMentions(ctx, m.Content, memID)
				}

				result.Results = append(result.Results, plugin.DataItem{
					DataType:  plugin.DataEntity,
					Operation: "create",
					Data: map[string]interface{}{
						"id":         memID,
						"content":    m.Content,
						"category":   cat,
						"tags":       tags,
						"confidence": m.Confidence,
						"created_at": time.Now().Format(time.RFC3339),
						"project":    thisChunkProject,
					},
					Confidence: m.Confidence,
				})
			}
		}
	}

	result.SourceIDs = allSourceIDs
	return result, nil
}

func (p *FactProcessor) extract(ctx context.Context, contents []string) ([]llm.ExtractedMemory, error) {
	return p.llm.Extract(ctx, llm.ExtractRequest{Contents: contents})
}

// chunkContents splits contents into chunks that fit within count and char limits.
// It also returns, per chunk, the projects of the source items that went into it
// (chunkProjects[i] corresponds to chunks[i]) so callers can stamp per-chunk project
// attribution rather than assuming one project for a whole multi-project session.
func chunkContents(contents []string, projects []string, maxCount, maxChars int) ([][]string, [][]string) {
	var chunks [][]string
	var chunkProjects [][]string
	var current []string
	var currentProjects []string
	chars := 0
	for idx, c := range contents {
		if len(current) >= maxCount || (chars+len(c) > maxChars && len(current) > 0) {
			chunks = append(chunks, current)
			chunkProjects = append(chunkProjects, currentProjects)
			current = nil
			currentProjects = nil
			chars = 0
		}
		current = append(current, c)
		if idx < len(projects) {
			currentProjects = append(currentProjects, projects[idx])
		}
		chars += len(c)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
		chunkProjects = append(chunkProjects, currentProjects)
	}
	return chunks, chunkProjects
}

// chunkProject returns the project to stamp onto memories extracted from one chunk.
// If every source item in the chunk shares a single non-empty project, that project
// wins (finest-grained provenance). Otherwise it falls back to sessionProject, which
// preserves the prior single-project-stamping behavior for ambiguous/multi-project chunks.
func chunkProject(chunkProjects []string, sessionProject string) string {
	if len(chunkProjects) == 0 {
		return sessionProject
	}
	first := chunkProjects[0]
	if first == "" {
		return sessionProject
	}
	for _, p := range chunkProjects[1:] {
		if p != first {
			return sessionProject
		}
	}
	return first
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
	// Within each session, sort by (prompt_id, created_at) so messages from the same
	// conversation turn — a user prompt and the assistant's reply share a prompt_id —
	// land adjacent. The extractor then sees the full Q&A context together, which yields
	// denser, more accurate memories than scattering related messages across chunks.
	for sid := range groups {
		groups[sid] = sortSessionByPrompt(groups[sid])
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

// sortSessionByPrompt orders a session's items so same-prompt_id messages are contiguous,
// ordered by created_at within the prompt. Items without a prompt_id keep time order but
// are grouped together at the start of their natural slot. This is a stable, deterministic
// reordering — it never drops or merges items.
func sortSessionByPrompt(items []plugin.InboxItem) []plugin.InboxItem {
	if len(items) <= 1 {
		return items
	}
	// Bucket by prompt_id (empty bucket for items without one), preserving arrival order.
	order := make([]string, 0, len(items))
	buckets := make(map[string][]plugin.InboxItem)
	for _, it := range items {
		if _, ok := buckets[it.PromptID]; !ok {
			order = append(order, it.PromptID)
		}
		buckets[it.PromptID] = append(buckets[it.PromptID], it)
	}
	// Sort each bucket by created_at so a turn's messages are in chronological order.
	for k := range buckets {
		b := buckets[k]
		sort.SliceStable(b, func(i, j int) bool {
			return b[i].CreatedAt.Before(b[j].CreatedAt)
		})
	}
	var out []plugin.InboxItem
	for _, k := range order {
		out = append(out, buckets[k]...)
	}
	return out
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
	id, _ := item.Data["id"].(string)
	content, _ := item.Data["content"].(string)
	cat, _ := item.Data["category"].(string)
	tagsRaw, _ := item.Data["tags"].([]string)
	confidence, _ := item.Data["confidence"].(float64)
	createdAt, _ := item.Data["created_at"].(string)
	project, _ := item.Data["project"].(string)

	if createdAt == "" {
		createdAt = time.Now().Format(time.RFC3339)
	}
	t, _ := time.Parse(time.RFC3339, createdAt)

	_ = confidence

	if id == "" {
		id = uuid.New().String()
	}

	return &store.Memory{
		ID:          id,
		Content:     content,
		ContentHash: store.HashContent(content),
		Phase:       store.PhaseProcessed,
		Category:    store.NormalizeCategory(store.Category(cat)),
		Scope:       "global",
		Tags:        tagsRaw,
		Source:      "fact-processor",
		Project:     project,
		CreatedAt:   t,
		UpdatedAt:   t,
		Version:     1,
	}
}
