package daemon

import (
	"testing"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

// newEvidenceTestStore returns a SqliteStore backed by an in-memory database, preloaded with a
// representative mix of verdict signal: a rejected proposal (with domain + reject_reason), an
// accepted proposal, a pending one (must be skipped), and old-format feedback (no verdict,
// must be skipped) plus a verdict-bearing feedback memory.
func newEvidenceTestStore(t *testing.T) *store.SqliteStore {
	t.Helper()
	s, err := store.NewSqliteStore(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	now := time.Now()

	cases := []*store.Memory{
		// Rejected proposal on the "Debugging" domain — carries a reject_reason.
		{Content: "fix zcode sync", Category: store.CategoryProposals, Phase: store.PhaseOrganized,
			Source: "makro-brain", CreatedAt: now,
			Metadata: map[string]any{"status": "rejected", "domain": "Debugging", "reject_reason": "dup"}},
		// Accepted proposal on the same domain → accept_rate should be 0.5 for Debugging.
		{Content: "add FTS index", Category: store.CategoryProposals, Phase: store.PhaseOrganized,
			Source: "makro-brain", CreatedAt: now,
			Metadata: map[string]any{"status": "accepted", "domain": "Debugging", "verdict_at": now.Format(time.RFC3339)}},
		// Pending proposal → must NOT contribute signal.
		{Content: "maybe later", Category: store.CategoryProposals, Phase: store.PhaseOrganized,
			Source: "makro-brain", CreatedAt: now,
			Metadata: map[string]any{"status": "pending", "domain": "Debugging"}},
		// Feedback with a verdict on a different domain.
		{Content: "rust proposal", Category: store.CategoryFeedback, Phase: store.PhaseOrganized,
			Source: "human", CreatedAt: now,
			Metadata: map[string]any{"verdict": "accept", "domain": "Languages", "proposal_id": "abc"}},
		// Old-format feedback with no verdict → must be skipped.
		{Content: "legacy feedback note", Category: store.CategoryFeedback, Phase: store.PhaseOrganized,
			Source: "human", CreatedAt: now, Metadata: map[string]any{}},
	}
	for _, m := range cases {
		if err := s.IngestMemory(m); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	return s
}

func TestEvidenceTaskBucketsByDomainAndComputesRate(t *testing.T) {
	s := newEvidenceTestStore(t)
	task := &EvidenceTask{Store: s}

	n, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Two distinct domains: "debugging" and "languages".
	if n != 2 {
		t.Fatalf("expected 2 domain memories written, got %d", n)
	}

	// Verify the Debugging memory: 1 accept + 1 reject → accept_rate 0.5, pending excluded.
	mem := findEvidenceMemory(t, s, "debugging")
	if got := metadataFloat(mem, "accept_rate"); got != 0.5 {
		t.Errorf("accept_rate = %v, want 0.5", got)
	}
	if got := metadataInt(mem, "verdict_count"); got != 2 {
		t.Errorf("verdict_count = %v, want 2", got)
	}
	ev := mem.Metadata["evidence"].([]any)
	if len(ev) != 2 {
		t.Errorf("evidence len = %d, want 2 (pending must be excluded)", len(ev))
	}

	// Verify Languages: 1 accept → accept_rate 1.0.
	memLang := findEvidenceMemory(t, s, "languages")
	if got := metadataFloat(memLang, "accept_rate"); got != 1.0 {
		t.Errorf("languages accept_rate = %v, want 1.0", got)
	}
}

func TestEvidenceTaskIsIdempotent(t *testing.T) {
	s := newEvidenceTestStore(t)
	task := &EvidenceTask{Store: s}

	if _, err := task.Run(s); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Second run must not create duplicate domain memories — it should update the existing ones.
	n2, err := task.Run(s)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n2 != 2 {
		t.Fatalf("second run wrote %d, want 2 (same domains, no new rows)", n2)
	}
	// Count evidence-task-owned preference memories: should still be exactly 2.
	rows, err := s.List(store.ListOptions{Category: store.CategoryPreferences, Source: "evidence-task", Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("evidence-task preference rows = %d, want 2", len(rows))
	}
}

// findEvidenceMemory loads the evidence-task preference memory for a domain by metadata.topic.
func findEvidenceMemory(t *testing.T, s *store.SqliteStore, domain string) *store.Memory {
	t.Helper()
	rows, err := s.List(store.ListOptions{Category: store.CategoryPreferences, Source: "evidence-task", Limit: 100})
	if err != nil {
		t.Fatalf("list preferences: %v", err)
	}
	for _, m := range rows {
		if d, _ := m.Metadata["topic"].(string); d == domain {
			return m
		}
	}
	t.Fatalf("no evidence memory for domain %q", domain)
	return nil
}

func metadataFloat(m *store.Memory, key string) float64 {
	v, ok := m.Metadata[key].(float64)
	if !ok {
		return -1
	}
	return v
}

func metadataInt(m *store.Memory, key string) int {
	// JSON round-trips numbers as float64, so accept both.
	switch v := m.Metadata[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return -1
}
