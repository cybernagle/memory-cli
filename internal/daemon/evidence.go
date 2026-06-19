package daemon

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

// EvidenceTask implements RECONCILE P3 #18: sediment accept/reject signal onto per-topic
// preference memories so the brain (makro) can query "what is the user's acceptance rate for
// topic X" without rescanning every proposal.
//
// It is the fine-grained counterpart to ProfileTask. ProfileTask writes ONE global character
// memory ("user overall likes Rust, rejects Python"). EvidenceTask writes MANY per-domain
// preference memories ("topic: Debugging — accept_rate 0.0, 1 reject: 'memory system duplicated'").
//
// Pipeline:
//  1. Scan proposals with a non-pending status (metadata.status ∈ accepted|rejected|ignored)
//     plus feedback memories carrying a verdict (metadata.verdict).
//  2. Bucket each verdict by its domain/topic.
//  3. For each domain: find or create a preference memory owned by this task
//     (source=evidence-task, metadata.topic=<domain>) and atomically merge in:
//     metadata.evidence      = [{type, proposal_id, topic, reason, at}, ...]
//     metadata.accept_rate   = accepted / (accepted+rejected+ignored)
//     metadata.verdict_count = N
//     metadata.last_updated  = <now>
//
// It is idempotent: rerunning recomputes from the raw verdicts, so correcting a mislabeled
// domain just requires fixing the source proposal. The per-topic memory content is a compact
// human-readable summary so it also surfaces in normal preference searches.
type EvidenceTask struct {
	Store *store.SqliteStore
}

func (t *EvidenceTask) Name() string { return "evidence" }

func (t *EvidenceTask) Run(s store.Store) (int, error) {
	if t.Store == nil {
		return 0, nil
	}

	// Gather verdict signal from both proposals and feedback. The two sources use slightly
	// different metadata shapes (proposals carry status+domain; feedback carries verdict+reason),
	// so we normalize both into a single []verdict before bucketing by domain.
	verdicts, err := t.gatherVerdicts()
	if err != nil {
		return 0, err
	}
	if len(verdicts) == 0 {
		return 0, nil
	}

	// Bucket by domain so each topic gets its own preference memory with its own accept rate.
	buckets := t.bucketByDomain(verdicts)

	written := 0
	for domain, vs := range buckets {
		if err := t.writeDomainEvidence(domain, vs); err != nil {
			// One bad domain shouldn't abort the rest — log and continue.
			log.Printf("[evidence] domain %q: %v", domain, err)
			continue
		}
		written++
	}
	if written > 0 {
		log.Printf("[evidence] sedimented %d verdicts across %d domains",
			len(verdicts), written)
	}
	return written, nil
}

// verdict is the normalized accept/reject signal drawn from proposals or feedback.
type verdict struct {
	Type       string // "accept" | "reject" | "ignore"
	ProposalID string
	Domain     string
	Reason     string
	Topic      string // best-effort content snippet for the human-readable summary
	At         string // RFC3339 timestamp of the verdict (verdict_at or memory created_at)
}

// gatherVerdicts pulls accept/reject/ignore signal from proposals (status state machine) and
// feedback memories (verdict field). Pending proposals and feedback without a verdict are
// skipped — they carry no fitting signal.
func (t *EvidenceTask) gatherVerdicts() ([]verdict, error) {
	proposals, err := t.Store.List(store.ListOptions{Category: store.CategoryProposals, Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	feedback, err := t.Store.List(store.ListOptions{Category: store.CategoryFeedback, Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}

	var out []verdict
	for _, p := range proposals {
		status := ""
		domain := "general"
		reason := ""
		at := p.CreatedAt.Format(time.RFC3339)
		if p.Metadata != nil {
			if s, ok := p.Metadata["status"].(string); ok && s != "" && s != "pending" {
				status = normalizeVerdict(s)
			}
			if d, ok := p.Metadata["domain"].(string); ok && d != "" {
				domain = d
			}
			if r, ok := p.Metadata["reject_reason"].(string); ok {
				reason = r
			}
			if v, ok := p.Metadata["verdict_at"].(string); ok && v != "" {
				at = v
			}
		}
		if status == "" {
			continue // pending or no status → no fitting signal
		}
		out = append(out, verdict{
			Type:       status,
			ProposalID: p.ID,
			Domain:     domain,
			Reason:     reason,
			Topic:      firstLine(p.Content),
			At:         at,
		})
	}

	for _, f := range feedback {
		verdictStr := ""
		reason := ""
		domain := "general"
		if f.Metadata != nil {
			if v, ok := f.Metadata["verdict"].(string); ok && v != "" {
				verdictStr = normalizeVerdict(v)
			}
			if r, ok := f.Metadata["reason"].(string); ok {
				reason = r
			}
			if d, ok := f.Metadata["domain"].(string); ok && d != "" {
				domain = d
			}
		}
		if verdictStr == "" {
			continue // old-format feedback with no verdict carries no fitting signal
		}
		out = append(out, verdict{
			Type:       verdictStr,
			ProposalID: metadataStr(f.Metadata, "proposal_id"),
			Domain:     domain,
			Reason:     reason,
			Topic:      firstLine(f.Content),
			At:         f.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// bucketByDomain groups verdicts by their domain/topic key. Sorted output for stable logging.
func (t *EvidenceTask) bucketByDomain(vs []verdict) map[string][]verdict {
	buckets := map[string][]verdict{}
	for _, v := range vs {
		key := strings.TrimSpace(v.Domain)
		if key == "" {
			key = "general"
		}
		key = strings.ToLower(key)
		buckets[key] = append(buckets[key], v)
	}
	return buckets
}

// writeDomainEvidence finds (or creates) the per-domain preference memory owned by this task,
// then atomically merges the recomputed evidence + accept_rate into its metadata. Content is
// rewritten so the memory is discoverable via normal content search too.
func (t *EvidenceTask) writeDomainEvidence(domain string, vs []verdict) error {
	// Stable summary keyed by domain, so we look up the existing row by topic.
	memID, err := t.findOrCreateTopicMemory(domain)
	if err != nil {
		return fmt.Errorf("find/create topic memory: %w", err)
	}

	// Build evidence list (most recent first) and aggregate stats.
	sort.Slice(vs, func(i, j int) bool { return vs[i].At > vs[j].At })
	evidence := make([]map[string]any, 0, len(vs))
	counts := map[string]int{"accept": 0, "reject": 0, "ignore": 0}
	for _, v := range vs {
		counts[v.Type]++
		ev := map[string]any{
			"type":        v.Type,
			"proposal_id": v.ProposalID,
			"topic":       v.Topic,
			"at":          v.At,
		}
		if v.Reason != "" {
			ev["reason"] = v.Reason
		}
		evidence = append(evidence, ev)
	}
	decided := counts["accept"] + counts["reject"] + counts["ignore"]
	acceptRate := 0.0
	if decided > 0 {
		acceptRate = float64(counts["accept"]) / float64(decided)
	}

	patch := map[string]any{
		"topic":         domain,
		"evidence":      evidence,
		"accept_rate":   acceptRate,
		"verdict_count": decided,
		"accepted":      counts["accept"],
		"rejected":      counts["reject"],
		"ignored":       counts["ignore"],
		"last_updated":  time.Now().Format(time.RFC3339),
		"source_task":   "evidence",
	}
	if err := t.Store.UpdateMemoryMetadata(memID, patch); err != nil {
		return fmt.Errorf("merge evidence: %w", err)
	}

	// Rewrite content to a compact summary so the memory shows up in preference searches and
	// is human-readable in the dashboard. Use a stable prefix so IngestMemory dedups resaves.
	summary := fmt.Sprintf("[topic: %s] accept_rate=%.2f (%d accept / %d reject / %d ignore) — %s",
		domain, acceptRate, counts["accept"], counts["reject"], counts["ignore"],
		summarizeTopics(vs))
	if _, err := t.Store.DB().Exec(
		"UPDATE memories SET content = ?, updated_at = ? WHERE id = ?",
		summary, time.Now().Format(time.RFC3339), memID,
	); err != nil {
		return fmt.Errorf("rewrite content: %w", err)
	}
	return nil
}

// findOrCreateTopicMemory returns the id of the evidence-task-owned preference memory for a
// domain, creating it on first encounter. Lookup is by source + metadata.topic so the row is
// stable across runs (idempotent upsert by domain). Uses metadata LIKE since proposal volume is
// low — no index needed (see ARCHITECTURE_DIAGNOSIS §5 / RECONCILE §5.2).
func (t *EvidenceTask) findOrCreateTopicMemory(domain string) (string, error) {
	// Match the topic field on our own rows only. Quoting the JSON value guards against quotes
	// in domain names; domains are LLM-generated category words so they are plain, but be safe.
	needle := fmt.Sprintf(`"topic":"%s"`, domain)
	row := t.Store.DB().QueryRow(
		"SELECT id FROM memories WHERE source = 'evidence-task' AND metadata LIKE ? LIMIT 1",
		"%"+needle+"%",
	)
	var id string
	if err := row.Scan(&id); err == nil && id != "" {
		return id, nil
	}

	// First time for this domain: insert a minimal row, then enrich via the normal path.
	mem := &store.Memory{
		Content:   fmt.Sprintf("[topic: %s] (pending evidence)", domain),
		Phase:     store.PhaseOrganized,
		Category:  store.CategoryPreferences,
		Scope:     "global",
		Tags:      []string{"evidence", "auto-generated"},
		Source:    "evidence-task",
		CreatedAt: time.Now(),
		Metadata:  map[string]any{"topic": domain, "source_task": "evidence"},
	}
	if err := t.Store.IngestMemory(mem); err != nil {
		return "", err
	}
	return mem.ID, nil
}

// normalizeVerdict maps the various status strings (from proposals.status and feedback.verdict)
// into the canonical accept/reject/ignore triplet used by the brain's fitting signal. Unknown
// values map to "ignore" so they are counted but don't inflate the accept rate.
func normalizeVerdict(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "accepted", "accept":
		return "accept"
	case "rejected", "reject":
		return "reject"
	case "ignored", "ignore":
		return "ignore"
	default:
		return "ignore"
	}
}

// summarizeTopics produces a short human-readable roll-up of the verdict subjects for the
// preference memory's content field. Caps at 3 to keep the line readable in the dashboard.
func summarizeTopics(vs []verdict) string {
	topics := make([]string, 0, 3)
	for _, v := range vs {
		if len(topics) >= 3 {
			break
		}
		t := strings.TrimSpace(v.Topic)
		if t == "" {
			continue
		}
		topics = append(topics, fmt.Sprintf("%s: %s", v.Type, t))
	}
	if len(vs) > 3 {
		topics = append(topics, fmt.Sprintf("+%d more", len(vs)-3))
	}
	return strings.Join(topics, "; ")
}

func metadataStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 70 {
		s = s[:70] + "..."
	}
	return s
}
