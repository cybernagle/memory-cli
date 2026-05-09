package daemon

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

// DreamLevel controls how deep the dream processing goes.
type DreamLevel int

const (
	DreamLight  DreamLevel = 1 // Classify inbox items by category
	DreamMedium DreamLevel = 2 // Classify + merge similar + resolve wikilinks
	DreamDeep   DreamLevel = 3 // Cross-category analysis + extract reminders
)

// DreamTask processes memories based on idle level.
type DreamTask struct {
	Level DreamLevel
}

func (t *DreamTask) Name() string { return fmt.Sprintf("dream-level-%d", t.Level) }

func (t *DreamTask) Run(s *store.Store) (int, error) {
	switch t.Level {
	case DreamLight:
		return dreamLight(s)
	case DreamMedium:
		return dreamMedium(s)
	case DreamDeep:
		return dreamDeep(s)
	default:
		return 0, fmt.Errorf("unknown dream level: %d", t.Level)
	}
}

// dreamLight classifies inbox memories into categories based on content heuristics.
func dreamLight(s *store.Store) (int, error) {
	memories, err := s.List(store.ListOptions{Phase: store.PhaseInbox})
	if err != nil {
		return 0, err
	}
	if len(memories) == 0 {
		return 0, nil
	}

	count := 0
	for _, mem := range memories {
		cat := classifyContent(mem.Content)
		if cat == "" {
			continue
		}
		// Re-write as organized with the detected category
		_, err := s.Write(mem.Content, store.PhaseOrganized, cat, mem.Scope, mem.Tags, mem.Source)
		if err != nil {
			log.Printf("dream: reclassify %s failed: %v", mem.ID[:8], err)
			continue
		}
		if err := s.Delete(mem.ID); err != nil {
			log.Printf("dream: delete inbox %s failed: %v", mem.ID[:8], err)
			continue
		}
		count++
	}
	return count, nil
}

// dreamMedium does light tasks plus merges similar memories and resolves wikilinks.
func dreamMedium(s *store.Store) (int, error) {
	lightCount, err := dreamLight(s)
	if err != nil {
		return lightCount, err
	}

	// Resolve [[wikilinks]] backlinks
	linkCount, err := s.ResolveBacklinks()
	if err != nil {
		return lightCount, err
	}

	// Merge similar content
	mergeCount := mergeSimilar(s)

	return lightCount + linkCount + mergeCount, nil
}

// dreamDeep does medium tasks plus cross-category analysis and reminder extraction.
func dreamDeep(s *store.Store) (int, error) {
	mediumCount, err := dreamMedium(s)
	if err != nil {
		return mediumCount, err
	}

	// Extract time commitments and create reminders
	reminderCount := extractReminders(s)

	return mediumCount + reminderCount, nil
}

// classifyContent uses simple heuristics to detect the category of content.
func classifyContent(content string) store.Category {
	lower := strings.ToLower(content)

	// People: mentions names with personal context
	if containsAny(lower, "他", "她", "喜欢", "讨厌", "偏好", "习惯说", "always says", "prefers", "hates", "doesn't like") {
		return store.CategoryPeople
	}

	// Feedback: contains evaluation language
	if containsAny(lower, "反馈", "建议", "改进", "不好用", "feedback", "suggestion", "improvement", "should") {
		return store.CategoryFeedback
	}

	// Decisions: contains decision language
	if containsAny(lower, "决定", "选择", "用这个", "decided", "chose", "going with", "we'll use") {
		return store.CategoryDecisions
	}

	// Lessons: contains learning language
	if containsAny(lower, "教训", "注意", "踩坑", "坑点", "lesson", "learned", "gotcha", "watch out", "make sure") {
		return store.CategoryLessons
	}

	// Preferences: contains preference language
	if containsAny(lower, "偏好", "习惯", "喜欢用", "prefer", "always use", "default to", "my favorite") {
		return store.CategoryPreferences
	}

	// Project: mentions project names, repos, or code paths
	if containsAny(lower, "项目", "仓库", "repo", "project", "feature", "branch", "PR", "deploy") {
		return store.CategoryProject
	}

	// Knowledge: technical content, facts
	if containsAny(lower, "原理", "机制", "架构", "算法", "how it works", "architecture", "pattern", "protocol", "API") {
		return store.CategoryKnowledge
	}

	// Date: time expressions
	if containsAny(lower, "明天", "下周", "月底", "tomorrow", "next week", "by friday", "deadline", "截止") {
		return store.CategoryDate
	}

	// Default: knowledge
	return store.CategoryKnowledge
}

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// mergeSimilar finds and merges memories with similar content prefixes.
func mergeSimilar(s *store.Store) int {
	all, err := s.List(store.ListOptions{Phase: store.PhaseOrganized})
	if err != nil {
		return 0
	}

	type best struct {
		id          string
		accessCount int
	}
	seen := make(map[string]*best)
	count := 0

	for _, mem := range all {
		prefix := strings.ToLower(strings.TrimSpace(mem.Content))
		if len(prefix) > 100 {
			prefix = prefix[:100]
		}
		if existing, dup := seen[prefix]; dup {
			if mem.AccessCount >= existing.accessCount {
				s.Delete(existing.id)
				seen[prefix] = &best{id: mem.ID, accessCount: mem.AccessCount}
			} else {
				s.Delete(mem.ID)
			}
			count++
		} else {
			seen[prefix] = &best{id: mem.ID, accessCount: mem.AccessCount}
		}
	}
	return count
}

// extractReminders scans for time commitments and creates reminder memories.
func extractReminders(s *store.Store) int {
	all, err := s.List(store.ListOptions{})
	if err != nil {
		return 0
	}

	reminderKeywords := []string{"明天", "下周", "月底", "tomorrow", "next week", "deadline", "截止", "提醒", "remind"}
	count := 0

	for _, mem := range all {
		lower := strings.ToLower(mem.Content)
		hasTime := false
		for _, kw := range reminderKeywords {
			if strings.Contains(lower, kw) {
				hasTime = true
				break
			}
		}
		if !hasTime {
			continue
		}
		// Check if a reminder already exists for this content
		hash := store.ExtractWikiLinks(mem.Content)
		_ = hash // placeholder — in a full implementation, we'd check for existing reminders

		// Create a reminder memory
		_, err := s.Write(
			fmt.Sprintf("[[自动提醒]] %s", mem.Content),
			store.PhaseOrganized,
			store.CategoryReminders,
			mem.Scope,
			append(mem.Tags, "auto-reminder"),
			"dream-engine",
		)
		if err == nil {
			count++
		}
	}
	return count
}

// IdleDetector determines dream level based on idle time.
type IdleDetector struct {
	lastWrite  time.Time
	thresholds map[DreamLevel]time.Duration
}

func NewIdleDetector() *IdleDetector {
	return &IdleDetector{
		thresholds: map[DreamLevel]time.Duration{
			DreamLight:  5 * time.Minute,
			DreamMedium: 30 * time.Minute,
			DreamDeep:   2 * time.Hour,
		},
	}
}

func (d *IdleDetector) Touch() {
	d.lastWrite = time.Now()
}

func (d *IdleDetector) Level() DreamLevel {
	if d.lastWrite.IsZero() {
		return DreamLight
	}
	idle := time.Since(d.lastWrite)
	switch {
	case idle >= d.thresholds[DreamDeep]:
		return DreamDeep
	case idle >= d.thresholds[DreamMedium]:
		return DreamMedium
	case idle >= d.thresholds[DreamLight]:
		return DreamLight
	default:
		return DreamLight
	}
}
