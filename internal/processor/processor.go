package processor

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

const (
	DefaultInboxThreshold = 100
	DefaultBatchSize      = 50
	flushInterval         = 10 // flush every N sessions
)

type Config struct {
	InboxThreshold int
	BatchSize      int
}

func (c Config) threshold() int {
	if c.InboxThreshold <= 0 {
		return DefaultInboxThreshold
	}
	return c.InboxThreshold
}

func (c Config) batchSize() int {
	if c.BatchSize <= 0 {
		return DefaultBatchSize
	}
	return c.BatchSize
}

type Processor struct {
	store   *store.SqliteStore
	llm     *llm.Client
	config  Config
	tracker *StatusTracker
}

func New(s *store.SqliteStore, llmClient *llm.Client, cfg Config) *Processor {
	return &Processor{store: s, llm: llmClient, config: cfg, tracker: GlobalTracker}
}

func (p *Processor) SetTracker(t *StatusTracker) {
	p.tracker = t
}

func (p *Processor) emit(eventType, msg string) {
	if p.tracker == nil {
		return
	}
	p.tracker.Emit(ProcessEvent{Type: eventType, Message: msg})
}

func (p *Processor) setStatus(phase, msg string) {
	if p.tracker == nil {
		return
	}
	p.tracker.Update(func(s *ProcessStatus) {
		s.Running = true
		s.Phase = phase
		s.Message = msg
	})
	p.emit("status", msg)
}

func (p *Processor) updateTrackerProgress(result *Result) {
	if p.tracker == nil {
		return
	}
	p.tracker.Update(func(s *ProcessStatus) {
		s.Progress.Layer1Input = result.Layer1Input
		s.Progress.Layer1Output = result.Layer1Output
		s.Progress.Layer2Input = result.Layer2Input
		s.Progress.Layer2Output = result.Layer2Output
		s.Progress.Organized = result.Organized
		s.Progress.Processed = result.Processed
	})
}

// ProcessInbox runs the two-layer extraction pipeline with incremental flushes.
func (p *Processor) ProcessInbox(ctx context.Context) (*Result, error) {
	inbox, err := p.store.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}

	if len(inbox) < p.config.threshold() {
		return &Result{Skipped: true, Reason: fmt.Sprintf("inbox %d < threshold %d", len(inbox), p.config.threshold())}, nil
	}

	log.Printf("[processor] starting: %d inbox memories", len(inbox))
	p.setStatus("extracting", fmt.Sprintf("Starting: %d inbox memories found", len(inbox)))
	if p.tracker != nil {
		p.tracker.Update(func(s *ProcessStatus) {
			s.Progress.TotalInbox = len(inbox)
		})
	}

	groups := groupBySession(inbox)
	if len(groups) == 0 {
		return nil, nil
	}

	result := &Result{GroupsProcessed: len(groups)}

	groupIdx := 0
	var batchExtracted []llm.ExtractedMemory
	var batchSourceIDs []map[string]bool

	for sid, mems := range groups {
		groupIdx++
		p.setStatus("extracting", fmt.Sprintf("Extracting session %s (%d/%d) — %d memories", sid, groupIdx, len(groups), len(mems)))
		if p.tracker != nil {
			p.tracker.Update(func(s *ProcessStatus) {
				s.Session = sid
				s.Progress.Layer1Input += len(mems)
			})
		}

		extracted, err := p.extractLayer(ctx, sid, mems)
		if err != nil {
			log.Printf("[processor] extract session %s: %v", sid, err)
			p.emit("error", fmt.Sprintf("Extract session %s failed: %v", sid, err))
			result.Errors++
			continue
		}
		if len(extracted) > len(mems) {
			trimTo := len(mems) - 1
			if trimTo < 1 {
				trimTo = 1
			}
			log.Printf("[processor] extract %d → %d, trimming to %d", len(mems), len(extracted), trimTo)
			extracted = extracted[:trimTo]
		}

		ids := make(map[string]bool)
		for _, m := range mems {
			ids[m.ID] = true
		}

		batchSourceIDs = append(batchSourceIDs, ids)
		batchExtracted = append(batchExtracted, extracted...)
		result.Layer1Input += len(mems)
		result.Layer1Output += len(extracted)
		p.updateTrackerProgress(result)
		p.emit("log", fmt.Sprintf("Session %s: %d → %d extracted", sid, len(mems), len(extracted)))

		// Flush batch every flushInterval sessions or at the end
		if len(batchExtracted) > 0 && (groupIdx%flushInterval == 0 || groupIdx == len(groups)) {
			if err := p.flushBatch(ctx, batchExtracted, batchSourceIDs, result); err != nil {
				log.Printf("[processor] flush batch error: %v", err)
				p.emit("error", fmt.Sprintf("Flush failed: %v", err))
			}
			batchExtracted = nil
			batchSourceIDs = nil
		}
	}

	msg := fmt.Sprintf("Done: %d inbox → %d extracted → %d merged → %d organized",
		result.Layer1Input, result.Layer1Output, result.Layer2Output, result.Organized)
	log.Printf("[processor] %s", msg)

	if p.tracker != nil {
		p.tracker.Update(func(s *ProcessStatus) {
			s.Running = false
			s.Phase = "idle"
			s.Message = msg
			s.Session = ""
		})
	}
	p.emit("done", msg)
	return result, nil
}

// flushBatch merges extracted memories and writes organized results, then deletes inbox sources.
func (p *Processor) flushBatch(ctx context.Context, extracted []llm.ExtractedMemory, sourceIDs []map[string]bool, result *Result) error {
	p.setStatus("merging", fmt.Sprintf("Merging %d extracted memories", len(extracted)))

	merged, err := p.mergeLayer(ctx, extracted)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	if len(merged) > len(extracted) {
		merged = merged[:len(extracted)]
	}
	result.Layer2Input += len(extracted)
	result.Layer2Output += len(merged)
	p.updateTrackerProgress(result)

	p.setStatus("writing", fmt.Sprintf("Writing %d organized memories", len(merged)))
	for _, m := range merged {
		categories := extractCategories(m.Content)
		cat := store.CategoryKnowledge
		if len(categories) > 0 {
			cat = store.Category(categories[0])
		}
		tags := m.Tags
		if len(tags) == 0 {
			tags = m.Categories
		}
		organized := &store.Memory{
			ID:          uuid.New().String(),
			Content:     m.Content,
			ContentHash: store.HashContent(m.Content),
			Phase:       store.PhaseOrganized,
			Category:    cat,
			Scope:       "global",
			Tags:        tags,
			Source:      "processor",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
			Version:     1,
			Links:       store.ExtractWikiLinks(m.Content),
		}
		if err := p.store.InsertMemory(organized); err != nil {
			log.Printf("[processor] write organized: %v", err)
			continue
		}
		result.Organized++
		p.updateTrackerProgress(result)
	}

	// Mark processed inbox memories (keep for audit, don't delete)
	for _, ids := range sourceIDs {
		for id := range ids {
			if err := p.store.MarkProcessed(id); err != nil {
				continue
			}
			result.Processed++
		}
	}
	p.updateTrackerProgress(result)
	p.emit("log", fmt.Sprintf("Flushed: %d → %d merged → %d organized, %d inbox processed",
		len(extracted), len(merged), result.Organized, result.Processed))

	return nil
}

func (p *Processor) extractLayer(ctx context.Context, sessionID string, mems []*store.Memory) ([]llm.ExtractedMemory, error) {
	var contents []string
	for _, m := range mems {
		contents = append(contents, m.Content)
	}

	batchSize := p.config.batchSize()
	if len(contents) > batchSize {
		var all []llm.ExtractedMemory
		for i := 0; i < len(contents); i += batchSize {
			end := i + batchSize
			if end > len(contents) {
				end = len(contents)
			}
			batch, err := p.llm.Extract(ctx, llm.ExtractRequest{Contents: contents[i:end]})
			if err != nil {
				return nil, err
			}
			all = append(all, batch...)
		}
		if len(all) > len(contents) {
			all = all[:len(contents)]
		}
		return all, nil
	}

	return p.llm.Extract(ctx, llm.ExtractRequest{Contents: contents})
}

func (p *Processor) mergeLayer(ctx context.Context, extracted []llm.ExtractedMemory) ([]llm.MergedMemory, error) {
	asMerged := make([]llm.MergedMemory, len(extracted))
	for i, e := range extracted {
		asMerged[i] = llm.MergedMemory{
			Content:    e.Content,
			Categories: e.Categories,
			Tags:       e.Tags,
			Confidence: e.Confidence,
		}
	}

	if len(asMerged) <= DefaultBatchSize {
		return p.llm.Merge(ctx, llm.MergeRequest{Memories: asMerged})
	}

	var result []llm.MergedMemory
	for i := 0; i < len(asMerged); i += DefaultBatchSize {
		end := i + DefaultBatchSize
		if end > len(asMerged) {
			end = len(asMerged)
		}
		merged, err := p.llm.Merge(ctx, llm.MergeRequest{Memories: asMerged[i:end]})
		if err != nil {
			return nil, err
		}
		result = append(result, merged...)
	}

	if len(result) > DefaultBatchSize {
		merged, err := p.llm.Merge(ctx, llm.MergeRequest{Memories: result})
		if err != nil {
			return nil, err
		}
		result = merged
	}

	return result, nil
}

type Result struct {
	Skipped         bool   `json:"skipped"`
	Reason          string `json:"reason,omitempty"`
	GroupsProcessed int    `json:"groups_processed"`
	Layer1Input     int    `json:"layer1_input"`
	Layer1Output    int    `json:"layer1_output"`
	Layer2Input     int    `json:"layer2_input"`
	Layer2Output    int    `json:"layer2_output"`
	Organized       int    `json:"organized"`
	Processed int    `json:"processed"`
	Errors          int    `json:"errors"`
}

func groupBySession(mems []*store.Memory) map[string][]*store.Memory {
	groups := make(map[string][]*store.Memory)
	var withSession, withoutSession []*store.Memory
	for _, m := range mems {
		if m.SessionID != "" {
			withSession = append(withSession, m)
		} else {
			withoutSession = append(withoutSession, m)
		}
	}
	for _, m := range withSession {
		groups[m.SessionID] = append(groups[m.SessionID], m)
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
