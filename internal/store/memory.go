package store

import (
	"time"
)

type MemoryType string

const (
	ShortTerm MemoryType = "short"
	LongTerm  MemoryType = "long"
)

type Memory struct {
	ID          string     `yaml:"id" json:"id"`
	Content     string     `yaml:"content" json:"content"`
	Type        MemoryType `yaml:"type" json:"type"`
	Scope       string     `yaml:"scope" json:"scope"`
	Tags        []string   `yaml:"tags" json:"tags"`
	Source      string     `yaml:"source" json:"source"`
	CreatedAt   time.Time  `yaml:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `yaml:"updated_at" json:"updated_at"`
	ExpiresAt   *time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	AccessCount int        `yaml:"access_count" json:"access_count"`
	Links       []string   `yaml:"links,omitempty" json:"links,omitempty"`
	Version     int        `yaml:"version" json:"version"`
}
