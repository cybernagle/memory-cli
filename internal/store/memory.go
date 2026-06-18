package store

import (
	"strings"
	"time"
)

type Phase string

const (
	PhaseInbox     Phase = "inbox"
	PhaseProcessed Phase = "processed"
	PhaseOrganized Phase = "organized"
)

type Category string

const (
	CategorySoul        Category = "soul"
	CategoryCharacter   Category = "character"
	CategoryPeople      Category = "people"
	CategoryProject     Category = "project"
	CategoryDate        Category = "date"
	CategoryKnowledge   Category = "knowledge"
	CategoryFeedback    Category = "feedback"
	CategoryPreferences Category = "preferences"
	CategoryDecisions   Category = "decisions"
	CategoryLessons     Category = "lessons"
	CategoryHabits      Category = "habits"
	CategorySkills      Category = "skills"
	CategoryInbox       Category = "inbox"
	CategoryReminders   Category = "reminders"
)

var AllCategories = []Category{
	CategorySoul, CategoryCharacter, CategoryPeople, CategoryProject,
	CategoryDate, CategoryKnowledge, CategoryFeedback, CategoryPreferences,
	CategoryDecisions, CategoryLessons, CategoryHabits, CategorySkills,
}

type Memory struct {
	ID           string     `yaml:"id" json:"id"`
	Content      string     `yaml:"content" json:"content"`
	ContentHash  string     `yaml:"content_hash" json:"content_hash"`
	Phase        Phase      `yaml:"phase" json:"phase"`
	Category     Category   `yaml:"category" json:"category"`
	Scope        string     `yaml:"scope" json:"scope"`
	Tags         []string   `yaml:"tags" json:"tags"`
	Source       string     `yaml:"source" json:"source"`
	SessionID    string     `yaml:"session_id,omitempty" json:"session_id,omitempty"`
	Project      string     `yaml:"project,omitempty" json:"project,omitempty"`
	CreatedAt    time.Time  `yaml:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `yaml:"updated_at" json:"updated_at"`
	ExpiresAt    *time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	AccessCount  int        `yaml:"access_count" json:"access_count"`
	Links        []string   `yaml:"links,omitempty" json:"links,omitempty"`
	Version      int        `yaml:"version" json:"version"`
	ProcessedBy  []string   `yaml:"processed_by" json:"processed_by"`
	ConsumedMask int64      `yaml:"consumed_mask" json:"consumed_mask"`

	// Provenance enrichment captured at ingest time. These let downstream aggregation
	// reconstruct conversation threading (uuid/parent_uuid/prompt_id) and filter by the
	// originating context (role/git_branch/model). They are the data-foundation fields that
	// make later memory-processing meaningful — without them, a memory is just an orphan string.
	MessageUUID string `yaml:"message_uuid,omitempty" json:"message_uuid,omitempty"` // this message's uuid in the source transcript
	ParentUUID  string `yaml:"parent_uuid,omitempty" json:"parent_uuid,omitempty"`   // preceding message uuid (context chain)
	Role        string `yaml:"role,omitempty" json:"role,omitempty"`                 // "user" | "assistant"
	GitBranch   string `yaml:"git_branch,omitempty" json:"git_branch,omitempty"`     // active git branch from the transcript
	Model       string `yaml:"model,omitempty" json:"model,omitempty"`               // model that produced an assistant turn
	PromptID    string `yaml:"prompt_id,omitempty" json:"prompt_id,omitempty"`       // groups all messages in one user prompt turn
}

// NormalizeCategory strips wiki-link brackets, lowercases, and maps known aliases to standard categories.
func NormalizeCategory(cat Category) Category {
	s := strings.Trim(string(cat), " []")
	s = camelToKebab(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")

	// Reject garbage values: wiki-link leakage (contains [ or ]) or absurdly long strings.
	// Real categories and project names are short and bracket-free. These artifacts come from
	// multiple [[wiki-links]] being concatenated into the category field.
	if strings.ContainsAny(s, "[]") || len(s) > 40 {
		return CategoryInbox
	}

	// Map known aliases to standard categories
	aliases := map[string]Category{
		"preference": CategoryPreferences,
		"decision":   CategoryDecisions,
		"lesson":     CategoryLessons,
		"habit":      CategoryHabits,
		"skill":      CategorySkills,
		"reminder":   CategoryReminders,
		"character":  CategoryCharacter,
		"soul":       CategorySoul,
	}
	if mapped, ok := aliases[s]; ok {
		return mapped
	}

	// Check if it matches a standard category
	for _, std := range AllCategories {
		if s == string(std) {
			return std
		}
	}
	if s == string(CategoryInbox) {
		return CategoryInbox
	}
	if s == string(CategoryReminders) {
		return CategoryReminders
	}

	// Non-standard categories stay as-is (project names, etc.)
	return Category(s)
}

func camelToKebab(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 && result[len(result)-1] != '-' {
				result = append(result, '-')
			}
			result = append(result, c+32)
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}
