package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

var (
	processLimit   int
	processProject string
	processSession string
	processDryRun  bool
)

// processCmd manually triggers the Extract+Merge pipeline on inbox memories. Unlike the daemon
// (which is threshold-gated, capped per tick, and silently no-ops when serve isn't running),
// this runs synchronously, prints progress, and supports project/session filtering — so you can
// process one codebase at a time and watch the quality.
var processCmd = &cobra.Command{
	Use:   "process",
	Short: "Extract + merge inbox memories into organized knowledge (manual batch)",
	Long: `Process raw inbox conversation into structured memories.

Runs the two-layer pipeline (Extract then Merge) on inbox memories, bypassing the
daemon's threshold/cap gates. Results are written as phase=processed; the source
inbox items are also flipped to phase=processed (kept for audit).

Examples:
  memory process                      # process all inbox (careful: 17k items)
  memory process --project makro       # only one codebase
  memory process --project makro --limit 200
  memory process --dry-run --limit 50  # preview what would be processed, no writes`,
	RunE: runProcess,
}

func init() {
	processCmd.Flags().IntVar(&processLimit, "limit", 0, "Max inbox memories to process (0 = all)")
	processCmd.Flags().StringVar(&processProject, "project", "", "Only process memories from this project")
	processCmd.Flags().StringVar(&processSession, "session", "", "Only process memories from this session id")
	processCmd.Flags().BoolVar(&processDryRun, "dry-run", false, "Show what would be processed without writing")
	rootCmd.AddCommand(processCmd)
}

func runProcess(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	s, err := store.NewSqliteStoreFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	// LLM client resolves the key from env / ~/.claude/settings.json (GLM-4.5-Flash by default).
	llmClient, err := llm.NewClient(llm.Config{})
	if err != nil {
		return fmt.Errorf("LLM client: %w (set GLM_API_KEY or MEMORY_LLM_API_KEY)", err)
	}
	fmt.Printf("LLM: model=%s base=%s\n", llmClient.Model(), llmClient.BaseURL())

	// Select inbox memories NOT yet processed by "process-cmd". We use the consumed_mask
	// bitmask (not phase) to track what's been done — the inbox phase is never changed,
	// preserving the original data. This makes re-runs idempotent: only unprocessed items
	// are picked up, so you can run `memory process` repeatedly without re-extracting.
	allUnconsumed, err := s.ListUnconsumed("process-cmd")
	if err != nil {
		return fmt.Errorf("list unconsumed: %w", err)
	}

	// Apply optional filters (ListUnconsumed doesn't accept project/session/limit params).
	var inbox []*store.Memory
	for _, m := range allUnconsumed {
		if processProject != "" && m.Project != processProject {
			continue
		}
		if processSession != "" && m.SessionID != processSession {
			continue
		}
		inbox = append(inbox, m)
		if processLimit > 0 && len(inbox) >= processLimit {
			break
		}
	}

	fmt.Printf("Selected %d unprocessed inbox memories", len(inbox))
	if processProject != "" {
		fmt.Printf(" (project=%s)", processProject)
	}
	if processSession != "" {
		fmt.Printf(" (session=%s)", processSession)
	}
	fmt.Println()

	if len(inbox) == 0 {
		fmt.Println("Nothing to process.")
		return nil
	}

	if processDryRun {
		fmt.Println("\n--- DRY RUN (no writes) ---")
		for i, m := range inbox {
			if i >= 10 {
				fmt.Printf("  ... and %d more\n", len(inbox)-10)
				break
			}
			preview := strings.ReplaceAll(m.Content, "\n", " ")
			if len(preview) > 90 {
				preview = preview[:90] + "..."
			}
			fmt.Printf("  [%s] %s\n", m.Role, preview)
		}
		return nil
	}

	ctx := context.Background()

	// Group by session so each Extract call sees coherent Q&A turns (user prompt + assistant
	// reply in the same session, ordered by prompt_id). This reuses the same grouping logic
	// as the daemon path.
	groups := groupInboxBySession(inbox)
	fmt.Printf("Grouped into %d sessions\n\n", len(groups))

	var allExtracted []llm.ExtractedMemory
	var allSourceIDs []string
	totalInput := 0

	gi := 0
	for sid, mems := range groups {
		gi++
		totalInput += len(mems)
		fmt.Printf("[%d/%d] session %s: %d turns → extracting... ", gi, len(groups), shortID(sid), len(mems))

		// Build contextual content: [user]/[assistant] labels + project header. This is the fix
		// for the quality bottleneck — without role labels the model can't tell questions from
		// answers (see plan H2).
		contents := buildContextualContents(mems)

		extracted, err := extractWithBatching(ctx, llmClient, contents, 50)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			continue
		}
		// Guard: extracted should be fewer than inputs. Trim if the model over-produced.
		if len(extracted) > len(contents) {
			extracted = extracted[:len(contents)]
		}
		fmt.Printf("%d extracted\n", len(extracted))

		for _, m := range mems {
			allSourceIDs = append(allSourceIDs, m.ID)
		}
		allExtracted = append(allExtracted, extracted...)
	}

	if len(allExtracted) == 0 {
		fmt.Println("\nNo memories extracted (all sessions produced empty results).")
		return nil
	}

	// Global merge: consolidate all extracted memories across sessions into dense summaries.
	fmt.Printf("\nMerging %d extracted memories...\n", len(allExtracted))
	merged, err := mergeWithBatching(ctx, llmClient, allExtracted, 50)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	fmt.Printf("Merged into %d organized memories\n", len(merged))

	// Write organized memories. Each carries provenance: the Merge prompt returns source_ids
	// but they're batch-local positions, unreliable for global mapping. Instead, after writing,
	// we link each organized memory back to ALL source inbox IDs of this run via LinkMemories
	// (the only path that persists to the links table — InsertMemory does not). This gives a
	// coarse but correct provenance trail: an organized memory → the inbox turns it came from.
	written := 0
	for _, m := range merged {
		organized := &store.Memory{
			Content:   m.Content,
			Phase:     store.PhaseProcessed,
			Category:  store.CategoryKnowledge,
			Scope:     "global",
			Tags:      m.Tags,
			Source:    "process-cmd",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Version:   1,
		}
		if len(m.Categories) > 0 {
			organized.Category = store.Category(strings.Trim(m.Categories[0], "[]"))
		}
		if err := s.IngestMemory(organized); err != nil {
			fmt.Fprintf(os.Stderr, "write organized: %v\n", err)
			continue
		}
		// Persist provenance links: organized → each source inbox memory.
		for _, srcID := range allSourceIDs {
			_ = s.LinkMemories(organized.ID, srcID)
		}
		// Check if this new organized memory supersedes an older one (same topic, same project).
		// Marks the old one's metadata.superseded_by so search results prefer the latest version.
		if superseded := s.CheckAndSupersede(organized); superseded > 0 {
			log.Printf("[process] %d older memories superseded by new organized %s", superseded, organized.ID[:8])
		}
		written++
	}

	// Source inbox items are LEFT at phase=inbox. The user's principle: never delete or
	// rephase original data — organized memories are additive, written as separate records
	// (source="process-cmd"). We track which inbox items have been processed via the
	// consumed_mask bitmask (ConsumerProcessCmd), so re-runs skip them without mutating phase.
	consumed := 0
	for _, id := range allSourceIDs {
		if err := s.MarkConsumed(id, "process-cmd"); err == nil {
			consumed++
		}
	}

	fmt.Printf("\n✓ Done: %d inbox → %d extracted → %d merged → %d organized written, %d inbox marked consumed\n",
		totalInput, len(allExtracted), len(merged), written, consumed)
	return nil
}

// buildContextualContents renders each turn with its role label and prefixes the session's
// project. This is what makes the model understand the Q&A flow — the core quality fix.
func buildContextualContents(mems []*store.Memory) []string {
	contents := make([]string, 0, len(mems))
	var project string
	for _, m := range mems {
		if m.Project != "" {
			project = m.Project
		}
		role := m.Role
		if role == "" {
			role = "note"
		}
		contents = append(contents, fmt.Sprintf("[%s]: %s", role, strings.TrimSpace(m.Content)))
	}
	if project != "" && len(contents) > 0 {
		contents[0] = fmt.Sprintf("[project: %s]\n%s", project, contents[0])
	}
	return contents
}

// extractWithBatching calls llm.Extract in batches of batchSize, accumulating results.
func extractWithBatching(ctx context.Context, c *llm.Client, contents []string, batchSize int) ([]llm.ExtractedMemory, error) {
	if len(contents) <= batchSize {
		return c.Extract(ctx, llm.ExtractRequest{Contents: contents})
	}
	var all []llm.ExtractedMemory
	for i := 0; i < len(contents); i += batchSize {
		end := i + batchSize
		if end > len(contents) {
			end = len(contents)
		}
		batch, err := c.Extract(ctx, llm.ExtractRequest{Contents: contents[i:end]})
		if err != nil {
			return all, err
		}
		all = append(all, batch...)
	}
	return all, nil
}

// mergeWithBatching calls llm.Merge in batches, then one final consolidation pass if the
// intermediate result is still large.
func mergeWithBatching(ctx context.Context, c *llm.Client, extracted []llm.ExtractedMemory, batchSize int) ([]llm.MergedMemory, error) {
	asMerged := make([]llm.MergedMemory, len(extracted))
	for i, e := range extracted {
		asMerged[i] = llm.MergedMemory{
			Content: e.Content, Categories: e.Categories, Tags: e.Tags, Confidence: e.Confidence,
		}
	}

	if len(asMerged) <= batchSize {
		return c.Merge(ctx, llm.MergeRequest{Memories: asMerged})
	}

	var all []llm.MergedMemory
	for i := 0; i < len(asMerged); i += batchSize {
		end := i + batchSize
		if end > len(asMerged) {
			end = len(asMerged)
		}
		batch, err := c.Merge(ctx, llm.MergeRequest{Memories: asMerged[i:end]})
		if err != nil {
			return all, err
		}
		all = append(all, batch...)
	}
	// One final consolidation pass across all batch results.
	if len(all) > batchSize {
		final, err := c.Merge(ctx, llm.MergeRequest{Memories: all})
		if err == nil {
			return final, nil
		}
	}
	return all, nil
}

// groupInboxBySession buckets inbox memories by session, ordering same-prompt turns together.
// Mirrors processor.groupBySession / factprocessor.groupBySession but lives here so the command
// is self-contained.
func groupInboxBySession(mems []*store.Memory) map[string][]*store.Memory {
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
	// Bucket no-session items by 30-min time windows.
	if len(withoutSession) > 0 {
		var bucketKey string
		for i, m := range withoutSession {
			if i == 0 || m.CreatedAt.Sub(withoutSession[i-1].CreatedAt) > 30*time.Minute {
				bucketKey = "time-" + m.CreatedAt.Format("20060102-150405")
			}
			groups[bucketKey] = append(groups[bucketKey], m)
		}
	}
	return groups
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:8] + "…"
	}
	return s
}
