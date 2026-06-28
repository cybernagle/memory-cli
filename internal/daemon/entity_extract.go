package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// EntityExtractionTask uses an LLM to extract named entities from memory content and
// populates the entity + entity_mentions tables. Unlike the wiki-link-based entity resolver
// (which only creates entities when content has [[xxx]] markup), this task finds entities
// in ANY text — company names, project names, tech terms, contract numbers, people names.
//
// This fills the coverage gap: the entity table had ~5% coverage (only wiki-linked terms)
// because GLM-Flash doesn't reliably produce [[wiki-links]]. This task scans organized +
// processed memories, asks the LLM "what entities are in this text?", and records them.
//
// Idempotent: each memory is processed once (tracked via ConsumerEntityExtract bitmask).
type EntityExtractionTask struct {
	LLM   *llm.Client
	Store *store.SqliteStore
	// Limit overrides entityExtractPerTick for this run. The one-off `memory entity-build`
	// command sets this high (e.g. 100000) to drain the whole backlog in a single pass; the
	// daemon leaves it 0 so the per-tick cap applies.
	Limit int
}

const (
	// entityExtractPerTick caps how many memories one daemon tick processes. GLM-4.5-Flash is
	// fast and free, and entities are extracted in batches (one LLM call per batch of
	// entityExtractBatchSize, not per memory), so this can be much higher than the original 20.
	// The corpus backlog was ~11k; at 20/tick × 1h interval that's ~24 days. At 100/tick it's
	// ~5 days, and a one-off `memory entity-build` run clears it in minutes.
	entityExtractPerTick   = 100 // memories processed per daemon tick
	entityExtractBatchSize = 5  // memories per LLM call
)

func (t *EntityExtractionTask) Name() string { return "entity-extract" }

func (t *EntityExtractionTask) Run(s store.Store) (int, error) {
	if t.LLM == nil || t.Store == nil {
		return 0, nil
	}

	// Find memories not yet processed by entity-extract, querying the backlog DIRECTLY.
	// The old code used List(Limit:500) ordered by created_at DESC — that only ever saw the
	// newest 500 memories, so ~11k of older backlog was UNREACHABLE no matter how many ticks ran
	// (the "backlog never shrinks" bug). ListUnconsumedInPhase queries by consumed_mask across
	// organized/processed, oldest-first, so the oldest backlog drains first.
	limit := t.Limit
	if limit == 0 {
		limit = entityExtractPerTick
	}
	entityStore := entity.NewEntityStore(t.Store.DB())
	pending, err := t.Store.ListUnconsumedInPhase("entity-extract",
		[]store.Phase{store.PhaseOrganized, store.PhaseProcessed}, limit)
	if err != nil {
		return 0, err
	}
	_ = entityStore // used later for Resolve/RecordMention

	if len(pending) == 0 {
		return 0, nil
	}

	log.Printf("[entity-extract] %d memories to process", len(pending))

	ctx := context.Background()
	extracted := 0

	for i := 0; i < len(pending); i += entityExtractBatchSize {
		end := i + entityExtractBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[i:end]

		// Build LLM prompt: ask for entities in each memory.
		var sb strings.Builder
		sb.WriteString(`Extract named entities from the following memories. Output a JSON array where each element is:
{"memory": <1-based index>, "entities": ["entity1", "entity2", ...]}

Rules:
- Extract: company names, project names, product names, technology names, contract numbers, person names, domain names
- Do NOT extract: common words, verbs, adjectives, question words
- Keep entities in their original language (Chinese stays Chinese)
- Keep entities short (1-10 characters usually)
- Each memory gets its own entry

Memories:
`)
		for j, m := range batch {
			content := m.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("\n%d. %s", j+1, content))
		}
		sb.WriteString("\n\nJSON array:")

		llmCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := t.LLM.Chat(llmCtx, sb.String())
		cancel()
		if err != nil {
			log.Printf("[entity-extract] LLM error: %v", err)
			continue
		}

		// Parse the response.
		var results []struct {
			Memory   int      `json:"memory"`
			Entities []string `json:"entities"`
		}
		jsonStr := extractJSONArray(resp)
		if err := json.Unmarshal([]byte(jsonStr), &results); err != nil {
			log.Printf("[entity-extract] parse error: %v, raw: %.200s", err, resp)
			// Mark as consumed anyway so we don't retry indefinitely.
			for _, m := range batch {
				s.MarkConsumed(m.ID, "entity-extract")
			}
			continue
		}

		// Record entities for each memory.
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 30*time.Second)
		for _, r := range results {
			idx := r.Memory - 1
			if idx < 0 || idx >= len(batch) {
				continue
			}
			mem := batch[idx]
			for _, entName := range r.Entities {
				entName = strings.TrimSpace(entName)
				if entName == "" || len(entName) > 50 {
					continue
				}
				// Resolve or create the entity.
				entityID, ok, _ := entityStore.Resolve(resolveCtx, entName)
				if !ok {
					e, err := entityStore.CreateEntity(resolveCtx, entName, classifyEntity(entName))
					if err != nil {
						continue // likely duplicate name
					}
					entityID = e.ID
				}
				// Record the mention (links entity ↔ memory).
				entityStore.RecordMention(resolveCtx, entityID, mem.ID, entName)
				extracted++
			}
			// Mark this memory as entity-extracted.
			s.MarkConsumed(mem.ID, "entity-extract")
		}
		resolveCancel()
	}

	log.Printf("[entity-extract] extracted %d entity mentions", extracted)
	return extracted, nil
}

// classifyEntity assigns a kind based on the entity name. This is a simple heuristic —
// can be improved later with LLM classification.
func classifyEntity(name string) string {
	lower := strings.ToLower(name)
	// Company indicators.
	if strings.Contains(lower, "公司") || strings.Contains(lower, "有限") ||
		strings.Contains(lower, "inc") || strings.Contains(lower, "ltd") ||
		strings.Contains(lower, "corp") || strings.Contains(lower, "gmbh") {
		return "company"
	}
	// Contract/document numbers (WS-2026-xxx, BVxxx, etc).
	if len(name) >= 4 {
		for _, r := range name {
			if r >= '0' && r <= '9' {
				return "identifier"
			}
		}
	}
	// Domain names.
	if strings.Contains(lower, ".com") || strings.Contains(lower, ".cn") ||
		strings.Contains(lower, ".io") || strings.Contains(lower, ".de") {
		return "domain"
	}
	// Tech indicators.
	techWords := []string{"api", "sdk", "css", "html", "json", "sql", "tls", "dns", "ssh"}
	for _, tw := range techWords {
		if lower == tw || strings.Contains(lower, tw) {
			return "technology"
		}
	}
	return "concept"
}

// extractJSONArray finds the first balanced [ ] in a string (LLM may add prose around it).
func extractJSONArray(s string) string {
	start := strings.Index(s, "[")
	if start < 0 {
		return "[]"
	}
	depth := 0
	for i := start; i < len(s); i++ {
		if s[i] == '[' {
			depth++
		} else if s[i] == ']' {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return "[]"
}
