package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// ProfileTask synthesizes a user profile from proposal accept/reject history. It scans all
// proposals (with metadata.status) and feedback memories, feeds them to the LLM, and writes
// a single category=character memory representing the current "what interests the user +
// their completion rate + recent leanings" snapshot. The brain (makro) reads this latest
// character memory to steer its next round of proposals.
//
// Triggered: after each batch of proposal feedback, or via cron (daily). It runs at most
// once per interval — if nothing changed since the last profile, it skips.
type ProfileTask struct {
	LLM   *llm.Client
	Store *store.SqliteStore
}

func (t *ProfileTask) Name() string { return "profile" }

func (t *ProfileTask) Run(s store.Store) (int, error) {
	if t.LLM == nil || t.Store == nil {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Gather proposal verdicts: each proposal's domain + status (from metadata).
	proposals, err := t.Store.List(store.ListOptions{Category: store.CategoryProposals, Limit: 500})
	if err != nil {
		return 0, fmt.Errorf("list proposals: %w", err)
	}

	// Also gather explicit feedback memories (richer signal: reject reasons, outcomes).
	feedback, err := t.Store.List(store.ListOptions{Category: store.CategoryFeedback, Limit: 500})
	if err != nil {
		return 0, fmt.Errorf("list feedback: %w", err)
	}

	// Skip if there's no signal to synthesize from.
	if len(proposals) == 0 && len(feedback) == 0 {
		return 0, nil
	}

	// Check staleness: if the latest proposal/feedback is older than the latest profile,
	// there's nothing new to synthesize — skip to avoid burning LLM tokens.
	if !t.hasNewSignal(proposals, feedback) {
		return 0, nil
	}

	// Build the LLM prompt from the raw signal.
	prompt := t.buildProfilePrompt(proposals, feedback)

	// Synthesize the profile via LLM (plain text, not JSON — it's a narrative summary).
	profile, err := t.LLM.Chat(ctx, prompt)
	if err != nil {
		return 0, fmt.Errorf("llm profile synthesis: %w", err)
	}
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return 0, nil
	}

	// Compute aggregate stats for the metadata (accept rate, domain distribution).
	stats := t.computeStats(proposals, feedback)
	statsBytes, _ := json.Marshal(stats)

	// Write the profile as a single character-category memory. Mark the previous profile
	// (if any) as superseded by setting metadata.superseded=true, so only the latest counts.
	t.supersedeOldProfiles()

	profileMem := &store.Memory{
		Content:   profile,
		Phase:     store.PhaseProcessed,
		Category:  store.CategoryCharacter,
		Scope:     "global",
		Tags:      []string{"profile", "auto-generated"},
		Source:    "profile-task",
		CreatedAt: time.Now(),
		Metadata: map[string]any{
			"type":          "user-profile",
			"generated_at":  time.Now().Format(time.RFC3339),
			"signal_count":  len(proposals) + len(feedback),
			"stats":         stats,
		},
	}
	// Restore statsBytes reference (used for logging below).
	_ = statsBytes

	if err := t.Store.IngestMemory(profileMem); err != nil {
		return 0, fmt.Errorf("write profile: %w", err)
	}

	log.Printf("[profile] synthesized from %d proposals + %d feedback → %s",
		len(proposals), len(feedback), profileMem.ID)
	return 1, nil
}

// hasNewSignal checks whether the latest proposal/feedback is newer than the latest profile.
// If not, there's no point re-synthesizing.
func (t *ProfileTask) hasNewSignal(proposals, feedback []*store.Memory) bool {
	// Find latest signal timestamp.
	var latestSignal time.Time
	for _, m := range proposals {
		if m.CreatedAt.After(latestSignal) {
			latestSignal = m.CreatedAt
		}
	}
	for _, m := range feedback {
		if m.CreatedAt.After(latestSignal) {
			latestSignal = m.CreatedAt
		}
	}

	// Find latest profile.
	profiles, err := t.Store.List(store.ListOptions{Category: store.CategoryCharacter, Limit: 1})
	if err != nil || len(profiles) == 0 {
		return true // no profile yet → definitely synthesize
	}
	return latestSignal.After(profiles[0].CreatedAt)
}

// buildProfilePrompt assembles the LLM input from raw proposal/feedback signal.
func (t *ProfileTask) buildProfilePrompt(proposals, feedback []*store.Memory) string {
	var sb strings.Builder
	sb.WriteString("You are a user-profile synthesis engine. Based on the proposal accept/reject history below, write a CONCISE user profile (3-6 sentences) answering:\n")
	sb.WriteString("1. What topics/domains does the user care about most?\n")
	sb.WriteString("2. What is their overall acceptance rate (accepts vs rejects vs ignores)?\n")
	sb.WriteString("3. What kinds of proposals do they tend to reject, and why (if reasons given)?\n")
	sb.WriteString("4. What are their recent leanings (last 10 verdicts)?\n\n")
	sb.WriteString("Write in the user's language (Chinese if the proposals are in Chinese). Be specific and honest — if the data is thin, say so.\n\n")

	sb.WriteString(fmt.Sprintf("=== Proposals (%d) ===\n", len(proposals)))
	for i, p := range proposals {
		status := "pending"
		domain := ""
		if p.Metadata != nil {
			if s, ok := p.Metadata["status"].(string); ok {
				status = s
			}
			if d, ok := p.Metadata["domain"].(string); ok {
				domain = d
			}
		}
		content := p.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		sb.WriteString(fmt.Sprintf("%d. [%s] %s", i+1, status, content))
		if domain != "" {
			sb.WriteString(fmt.Sprintf(" (domain: %s)", domain))
		}
		sb.WriteString("\n")
	}

	if len(feedback) > 0 {
		sb.WriteString(fmt.Sprintf("\n=== Feedback (%d) ===\n", len(feedback)))
		for i, f := range feedback {
			verdict := ""
			reason := ""
			if f.Metadata != nil {
				if v, ok := f.Metadata["verdict"].(string); ok {
					verdict = v
				}
				if r, ok := f.Metadata["reason"].(string); ok {
					reason = r
				}
			}
			content := f.Content
			if len(content) > 60 {
				content = content[:60] + "..."
			}
			line := fmt.Sprintf("%d. [%s] %s", i+1, verdict, content)
			if reason != "" {
				line += fmt.Sprintf(" — reason: %s", reason)
			}
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n=== Profile (output) ===\n")
	return sb.String()
}

// computeStats derives aggregate numbers from the raw signal for the profile metadata.
func (t *ProfileTask) computeStats(proposals, feedback []*store.Memory) map[string]any {
	counts := map[string]int{"accepted": 0, "rejected": 0, "ignored": 0, "pending": 0}
	domains := map[string]int{}

	for _, p := range proposals {
		status := "pending"
		domain := "unknown"
		if p.Metadata != nil {
			if s, ok := p.Metadata["status"].(string); ok {
				status = s
			}
			if d, ok := p.Metadata["domain"].(string); ok && d != "" {
				domain = d
			}
		}
		counts[status]++
		domains[domain]++
	}

	total := len(proposals)
	acceptRate := 0.0
	if total > 0 {
		acceptRate = float64(counts["accepted"]) / float64(total)
	}

	return map[string]any{
		"total_proposals":  total,
		"accepted":         counts["accepted"],
		"rejected":         counts["rejected"],
		"ignored":          counts["ignored"],
		"pending":          counts["pending"],
		"accept_rate":      acceptRate,
		"domain_distribution": domains,
		"feedback_count":   len(feedback),
	}
}

// supersedeOldProfiles marks all existing character-category profiles as superseded so only
// the newest one is treated as current. The old ones are kept for history (not deleted).
func (t *ProfileTask) supersedeOldProfiles() {
	old, err := t.Store.List(store.ListOptions{Category: store.CategoryCharacter, Limit: 100})
	if err != nil {
		return
	}
	for _, p := range old {
		// Only supersede auto-generated profiles, not manually written character notes.
		for _, tag := range p.Tags {
			if tag == "auto-generated" {
				t.Store.UpdateMemoryMetadata(p.ID, map[string]any{"superseded": true})
				break
			}
		}
	}
}
