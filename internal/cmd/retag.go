package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/ingest"
	"github.com/cybernagle/memory-cli/internal/store"
)

var retagDBPath string

// retagCmd backfills semantic keyword tags for all existing memories and rebuilds
// the entity_mentions graph. Both operations are idempotent. Intended as a one-time
// (or occasional) backfill after enabling auto-keyword tagging / fixing the entity bug.
var retagCmd = &cobra.Command{
	Use:   "retag",
	Short: "Backfill keyword tags + rebuild entity mentions for all memories",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := retagDBPath
		if dbPath == "" {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dbPath = cfg.Storage.SQLitePath
			if dbPath == "" {
				dbPath = config.SQLiteDefaultPath()
			}
		}

		s, err := store.NewSqliteStore(dbPath)
		if err != nil {
			return fmt.Errorf("open sqlite: %w", err)
		}
		defer s.Close()
		db := s.DB()

		// --- 1. Keyword backfill: add content-derived tags to every memory ---
		rows, err := db.Query("SELECT id, content, category, project, processed_by FROM memories")
		if err != nil {
			return fmt.Errorf("query memories: %w", err)
		}
		type mem struct{ id, content, category, project, processedBy string }
		var all []mem
		for rows.Next() {
			var m mem
			if err := rows.Scan(&m.id, &m.content, &m.category, &m.project, &m.processedBy); err != nil {
				rows.Close()
				return err
			}
			all = append(all, m)
		}
		rows.Close()

		tagsAdded := 0
		for _, m := range all {
			for _, kw := range store.ExtractKeywords(m.content) {
				res, err := db.Exec("INSERT OR IGNORE INTO tags (memory_id, tag) VALUES (?, ?)", m.id, kw)
				if err != nil {
					return fmt.Errorf("insert tag: %w", err)
				}
				if n, _ := res.RowsAffected(); n > 0 {
					tagsAdded++
				}
			}
		}
		fmt.Printf("Keyword backfill: scanned %d memories, added %d tags\n", len(all), tagsAdded)

		// --- 2. Category cleanup: collapse the fragmented category field to the 13 standard types ---
		// The category field had two failure modes: (a) ingest hardcoded everything to knowledge,
		// (b) LLM processing pushed project/topic names (car-agent, makro, ios-slam-builder...) into
		// it, producing 600+ fragmented values. Fix: if the current category is NOT one of the
		// standard types, treat it as a misplaced topic tag — preserve it by adding it to tags —
		// then re-derive a proper type category from content. Standard-type categories are kept.
		isStandard := make(map[string]bool, len(store.AllCategories)+2)
		for _, c := range store.AllCategories {
			isStandard[string(c)] = true
		}
		isStandard[string(store.CategoryInbox)] = true
		isStandard[string(store.CategoryReminders)] = true

		catsChanged := 0
		topicsMigrated := 0
		for _, m := range all {
			cur := store.Category(m.category)
			curStr := string(cur)
			var next store.Category
			if isStandard[curStr] {
				// Already a standard type — keep it, but still normalize casing/aliases.
				next = store.NormalizeCategory(cur)
			} else {
				// Non-standard (a project/topic name leaked in). Preserve it as a tag so no
				// information is lost, then re-derive the type from content.
				if curStr != "" {
					if _, err := db.Exec("INSERT OR IGNORE INTO tags (memory_id, tag) VALUES (?, ?)", m.id, curStr); err != nil {
						return fmt.Errorf("migrate topic tag: %w", err)
					}
					topicsMigrated++
				}
				next = store.CategorizeContent(m.content)
			}
			if next != cur {
				if _, err := db.Exec("UPDATE memories SET category = ? WHERE id = ?", string(next), m.id); err != nil {
					return fmt.Errorf("update category: %w", err)
				}
				catsChanged++
			}
		}
		fmt.Printf("Category cleanup: %d memories re-categorized, %d topic values migrated to tags\n", catsChanged, topicsMigrated)

		// --- 3. Project backfill: recover project for memories missing it ---
		// The project name was historically dumped into tags (e.g. "Makro", "car-agent").
		// Build the set of known projects from ~/.claude/projects/* + adapter projects, then
		// for each memory with empty project, take the first tag that matches a known project.
		known := buildKnownProjects()
		// tagsByID[memoryID] = list of tags
		tagsByID := make(map[string][]string)
		if trows, err := db.Query("SELECT memory_id, tag FROM tags"); err == nil {
			for trows.Next() {
				var mid, tag string
				trows.Scan(&mid, &tag)
				tagsByID[mid] = append(tagsByID[mid], tag)
			}
			trows.Close()
		}
		projectsRecovered := 0
		for _, m := range all {
			if m.project != "" {
				continue
			}
			for _, tag := range tagsByID[m.id] {
				p := strings.ToLower(strings.TrimSpace(tag))
				if known[p] {
					if _, err := db.Exec("UPDATE memories SET project = ? WHERE id = ?", p, m.id); err != nil {
						return fmt.Errorf("update project: %w", err)
					}
					projectsRecovered++
					break
				}
			}
		}
		fmt.Printf("Project backfill: recovered project for %d memories (from %d known projects)\n", projectsRecovered, len(known))

		// --- 4. consumed_mask backfill: translate legacy consumption signals → bitmask ---
		// Before consumed_mask existed, consumption was tracked via processed_by (fact-processor)
		// and marker tags (consolidated/re-consolidated/concept-tagged). Translate those into the
		// bits so the new mechanism starts with full history. OR-accumulates (idempotent, atomic,
		// never clobbers bits already set by the live system).
		maskSet := 0
		for _, m := range all {
			var mask int64
			if strings.Contains(m.processedBy, "fact-processor") {
				mask |= int64(store.ConsumerFactProcessor)
			}
			for _, tag := range tagsByID[m.id] {
				switch strings.ToLower(strings.TrimSpace(tag)) {
				case "consolidated", "re-consolidated":
					mask |= int64(store.ConsumerConsolidateLLM)
				case "concept-tagged":
					mask |= int64(store.ConsumerEnrichTags)
				}
			}
			if mask != 0 {
				// Only set rows not yet touched by the bitmask (consumed_mask = 0). Since this
				// backfill runs before the live daemon, every row is 0 here; the guard makes the
				// count honest (0 on a re-run) and never clobbers bits the live system set later.
				res, err := db.Exec("UPDATE memories SET consumed_mask = ? WHERE id = ? AND consumed_mask = 0", mask, m.id)
				if err != nil {
					return fmt.Errorf("update consumed_mask: %w", err)
				}
				if n, _ := res.RowsAffected(); n > 0 {
					maskSet++
				}
			}
		}
		fmt.Printf("Consumed-mask backfill: set bits on %d memories\n", maskSet)

		// --- 5. Entity rebuild: drop all (derived) mentions and reconstruct from wiki-links ---
		// entity_mentions is fully derivable from [[wiki-links]] in content, so a clean rebuild
		// is lossless and idempotent. This also clears the memory_id="" rows from the old bug.
		// Build the resolver the same way serve.go does.
		entityComp := entity.NewEntityComponent()
		if err := entityComp.Init(context.Background(), db); err != nil {
			return fmt.Errorf("entity init: %w", err)
		}
		resolver := entity.NewResolver(entityComp)

		del, _ := db.Exec("DELETE FROM entity_mentions")
		badRows, _ := del.RowsAffected()
		// TODO(code-review): after deleting mentions, entities whose mention count drops to 0
		// are left orphaned. Consider pruning them (DELETE FROM entities WHERE id NOT IN
		// (SELECT entity_id FROM entity_mentions)) to keep the entity table honest. Not done
		// here because it changes rebuild semantics and needs its own idempotency tests.

		ctx := context.Background()
		mentionsRebuilt := 0
		for _, m := range all {
			links := store.ExtractWikiLinks(m.content)
			if len(links) == 0 {
				continue
			}
			resolved, err := resolver.ResolveMentions(ctx, m.content, m.id)
			if err != nil {
				continue
			}
			mentionsRebuilt += len(resolved)
		}
		fmt.Printf("Entity rebuild: cleared %d old mentions, rebuilt %d mentions\n", badRows, mentionsRebuilt)

		fmt.Println("Done.")
		return nil
	},
}

func init() {
	retagCmd.Flags().StringVar(&retagDBPath, "db", "", "SQLite DB path (default: from config)")
	rootCmd.AddCommand(retagCmd)
}

// buildKnownProjects returns the set of known project names (lowercased) derived from
// ~/.claude/projects/* directory names plus adapter-fixed projects. Used to recover the
// project field for existing memories by matching their tags.
func buildKnownProjects() map[string]bool {
	known := map[string]bool{
		"car-agent":   true,
		"fingersaver": true,
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return known
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects"))
	if err != nil {
		return known
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p := ingest.ProjectFromClaudeDir(e.Name()); p != "" {
			known[p] = true
		}
	}
	return known
}
