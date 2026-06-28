package daemon

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/llm"
)

// ReclassifyEntities re-judges the kind (concept/technology/domain/company/...) of every entity
// via the LLM, fixing the heuristic mislabeling from classifyEntity() (issue #1: 96% concept).
//
// The original extraction classifies kind with a narrow regex/word-list heuristic, so genuinely
// useful entities like go/sqlite/react/wails land in the concept bucket. Resolve() skips
// already-existing entities (no re-classify on re-extract), so the mislabeling is permanent
// without an explicit pass. This does that pass: batch the entity names to the LLM, get back
// {name: kind}, and UPDATE each.
//
// Idempotent: re-running produces stable kinds (the LLM is deterministic for this task, and
// the kind set is fixed). Batched so one LLM call classifies ~50 entities, not 8000 calls.
//
// kinds: the fixed taxonomy. concept is the fallback for anything that doesn't fit a more
// specific bucket.
var entityKindTaxonomy = []string{
	"technology",   // programming languages, frameworks, tools: go, react, sqlite, docker
	"product",      // named software products: makro, memory-cli, fingersaver, wails
	"company",      // organizations: 橘粒科技, 瑞福莱, openai, google
	"person",       // people: names (if any surface)
	"domain",       // knowledge areas / fields: encryption, finance, frontend
	"identifier",   // IDs, codes, URLs, contract numbers
	"concept",      // everything else (fallback)
}

const entityReclassifyBatch = 50

// ReclassifyResult is the outcome of a reclassification pass.
type ReclassifyResult struct {
	Total      int            // entities examined
	Reclassified int          // entities whose kind changed
	ByKind     map[string]int // final distribution
}

// ReclassifyEntities runs the full reclassification. kindFilter ("concept") restricts to one
// bucket — useful to only re-judge the suspect concept entities and leave already-correct
// technology/domain rows alone.
func ReclassifyEntities(ctx context.Context, es *entity.EntityStore, llmClient *llm.Client, kindFilter string) (*ReclassifyResult, error) {
	all, err := es.AllEntities(ctx, kindFilter)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return &ReclassifyResult{ByKind: map[string]int{}}, nil
	}

	reclassified := 0
	// Process in batches of entityReclassifyBatch — one LLM call per batch.
	for start := 0; start < len(all); start += entityReclassifyBatch {
		end := start + entityReclassifyBatch
		if end > len(all) {
			end = len(all)
		}
		batch := all[start:end]

		judged, err := classifyEntityBatch(ctx, llmClient, batch)
		if err != nil {
			log.Printf("[entity-reclassify] batch %d-%d error: %v (skipping)", start, end, err)
			continue
		}

		for _, e := range batch {
			newKind, ok := judged[strings.ToLower(e.Name)]
			if !ok || newKind == "" {
				continue // LLM didn't return this one; leave as-is
			}
			if newKind == e.Kind {
				continue // unchanged
			}
			if err := es.UpdateKind(ctx, e.ID, newKind); err != nil {
				log.Printf("[entity-reclassify] update %q: %v", e.Name, err)
				continue
			}
			reclassified++
		}

		// Progress log every batch.
		log.Printf("[entity-reclassify] processed %d/%d entities (%d reclassified so far)", end, len(all), reclassified)
	}

	// Final distribution.
	final, _ := es.AllEntities(ctx, "")
	dist := map[string]int{}
	for _, e := range final {
		dist[e.Kind]++
	}
	return &ReclassifyResult{Total: len(all), Reclassified: reclassified, ByKind: dist}, nil
}

// classifyEntityBatch asks the LLM to assign a kind to each entity name in one call. Returns a
// map of lowercased-name → kind. The prompt is strict (output JSON only, fixed taxonomy, no
// explanation) to keep parsing robust.
func classifyEntityBatch(ctx context.Context, c *llm.Client, batch []*entity.Entity) (map[string]string, error) {
	var sb strings.Builder
	sb.WriteString("Classify each entity into exactly ONE kind from this taxonomy:\n")
	for _, k := range entityKindTaxonomy {
		sb.WriteString("- " + k + "\n")
	}
	sb.WriteString("\nRules:\n")
	sb.WriteString("- technology = languages/frameworks/tools (go, react, sqlite, docker, git)\n")
	sb.WriteString("- product = named software/app projects (makro, memory-cli, wails, electron)\n")
	sb.WriteString("- company = organizations/businesses (橘粒科技, 瑞福莱, openai)\n")
	sb.WriteString("- person = people's names\n")
	sb.WriteString("- domain = knowledge fields/areas (encryption, frontend, finance)\n")
	sb.WriteString("- identifier = codes/IDs/URLs (contract numbers, uuids)\n")
	sb.WriteString("- concept = only when nothing else fits\n")
	sb.WriteString("\nEntities to classify:\n")
	for _, e := range batch {
		sb.WriteString(e.Name + "\n")
	}
	sb.WriteString("\nOutput a JSON object mapping each entity (lowercased) to its kind. JSON only, no explanation.\nExample: {\"go\":\"technology\",\"makro\":\"product\",\"瑞福莱\":\"company\"}")

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.ChatWithModel(cctx, "glm-4.7-flash", sb.String(), 1500)
	if err != nil {
		return nil, err
	}
	return parseEntityKinds(resp), nil
}

// parseEntityKinds extracts the {name: kind} map from the LLM response. Handles the usual LLM
// wrapping quirks (markdown fences, prose around the JSON) by extracting the first {...} block.
func parseEntityKinds(resp string) map[string]string {
	out := map[string]string{}
	// Find the JSON object in the response.
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return out
	}
	jsonStr := resp[start : end+1]
	// Parse without a strict struct — keys are dynamic. Walk the JSON manually to tolerate
	// LLM quirks (unquoted keys, trailing commas).
	lines := strings.Split(jsonStr, ",")
	for _, line := range lines {
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := cleanJSONToken(line[:colon])
		val := cleanJSONToken(line[colon+1:])
		if key == "" || val == "" {
			continue
		}
		// Validate kind is in taxonomy; ignore garbage.
		for _, k := range entityKindTaxonomy {
			if val == k {
				out[strings.ToLower(key)] = val
				break
			}
		}
	}
	return out
}

func cleanJSONToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`,{}[] \n\r\t")
	return s
}
