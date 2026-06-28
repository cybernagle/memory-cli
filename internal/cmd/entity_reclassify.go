package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

var reclassifyKindFilter string

// entityReclassifyCmd runs the one-off entity kind reclassification (issue #1). The original
// extraction used a narrow heuristic that mislabeled ~86% of entities as `concept`; this asks
// the LLM to re-judge each entity's kind with a richer taxonomy (technology/product/company/
// person/domain/identifier/concept) and force-overwrites via UpdateKind.
//
// Idempotent: re-running produces stable kinds. Safe — entities are derived from content, so
// misjudgments can be corrected by re-running. Default re-judges only `concept` entities (the
// suspect bucket); --all re-judges everything.
var entityReclassifyCmd = &cobra.Command{
	Use:   "entity-reclassify",
	Short: "One-off: re-classify entity kinds via LLM (fixes the concept-heavy mislabeling)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		dbPath := cfg.Storage.SQLitePath
		if dbPath == "" {
			dbPath = config.SQLiteDefaultPath()
		}
		s, err := store.NewSqliteStore(dbPath)
		if err != nil {
			return fmt.Errorf("open sqlite: %w", err)
		}
		defer s.Close()

		llmClient, err := llm.NewClient(llm.Config{})
		if err != nil {
			return fmt.Errorf("LLM client (need MEMORY_LLM_API_KEY): %w", err)
		}

		es := entity.NewEntityStore(s.DB())
		ctx := context.Background()

		// Before snapshot for the diff report.
		before, _ := es.AllEntities(ctx, "")
		beforeDist := map[string]int{}
		for _, e := range before {
			beforeDist[e.Kind]++
		}
		fmt.Printf("Before: %d entities — %s\n", len(before), distString(beforeDist))

		filter := reclassifyKindFilter
		if !reclassifyAll {
			// Default: only re-judge concept (the mislabeled bucket). technology/domain/etc were
			// heuristic-classified too, but they're usually correct (the heuristic is conservative
			// — it only assigns technology when confident). Re-judging only concept is cheaper.
			filter = "concept"
		}
		fmt.Printf("Re-classifying (filter=%q)...\n", filter)

		start := time.Now()
		result, err := daemon.ReclassifyEntities(ctx, es, llmClient, filter)
		if err != nil {
			return fmt.Errorf("reclassify: %w", err)
		}

		fmt.Printf("\nDone in %.0fs: examined %d, reclassified %d.\n",
			time.Since(start).Seconds(), result.Total, result.Reclassified)
		fmt.Printf("After: %s\n", distString(result.ByKind))
		return nil
	},
}

var reclassifyAll bool

func init() {
	entityReclassifyCmd.Flags().BoolVar(&reclassifyAll, "all", false, "re-classify ALL entities (default: only concept)")
	entityReclassifyCmd.Flags().StringVar(&reclassifyKindFilter, "kind", "", "re-classify only this kind (overrides default concept-only)")
	rootCmd.AddCommand(entityReclassifyCmd)
}

// distString renders a kind→count map as a compact inline summary.
func distString(dist map[string]int) string {
	if len(dist) == 0 {
		return "(empty)"
	}
	// Stable order by count desc.
	type kv struct {
		k string
		v int
	}
	var pairs []kv
	for k, v := range dist {
		pairs = append(pairs, kv{k, v})
	}
	// Simple desc sort.
	for i := 0; i < len(pairs); i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].v > pairs[i].v {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}
	var out string
	for i, p := range pairs {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%d", p.k, p.v)
	}
	return out
}
