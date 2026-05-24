package store

import (
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
	ID          string     `yaml:"id" json:"id"`
	Content     string     `yaml:"content" json:"content"`
	ContentHash string     `yaml:"content_hash" json:"content_hash"`
	Phase       Phase      `yaml:"phase" json:"phase"`
	Category    Category   `yaml:"category" json:"category"`
	Scope       string     `yaml:"scope" json:"scope"`
	Tags        []string   `yaml:"tags" json:"tags"`
	Source      string     `yaml:"source" json:"source"`
	SessionID   string     `yaml:"session_id,omitempty" json:"session_id,omitempty"`
	CreatedAt   time.Time  `yaml:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `yaml:"updated_at" json:"updated_at"`
	ExpiresAt   *time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	AccessCount int        `yaml:"access_count" json:"access_count"`
	Links       []string   `yaml:"links,omitempty" json:"links,omitempty"`
	Version     int        `yaml:"version" json:"version"`
}
