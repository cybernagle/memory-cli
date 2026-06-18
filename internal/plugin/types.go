package plugin

import (
	"context"
	"time"
)

// DataType identifies the kind of structured data a component manages.
type DataType string

const (
	DataEntity   DataType = "entity"
	DataRelation DataType = "relation"
	DataEvent    DataType = "event"
	DataRule     DataType = "rule"
	DataContext  DataType = "context"
)

// DecayPolicy defines how a component's data should age.
type DecayPolicy struct {
	Strategy   string // "time-based", "access-count", "none"
	HalfLife   string // e.g. "720h" for 30 days
	NeverPurge bool   // if true, never delete, only downgrade
}

// InboxItem represents a raw inbox memory to be processed.
type InboxItem struct {
	ID        string
	Content   string
	SessionID string
	Project   string
	PromptID  string // groups messages from one user-prompt turn (context signal)
	Source    string
	Tags      []string
	CreatedAt time.Time
}

// DataItem is a generic structured data item produced by a processor.
type DataItem struct {
	DataType   DataType
	Operation  string                 // "create", "update", "merge"
	Data       map[string]interface{} // component-specific payload
	SourceID   string                 // originating inbox item
	Confidence float64
}

// ProcessInput is what the pipeline feeds into a processor.
type ProcessInput struct {
	Items      []InboxItem
	Components ComponentResolver
}

// ProcessOutput is what a processor produces.
type ProcessOutput struct {
	Results   []DataItem
	SourceIDs []string // inbox items consumed (will be marked processed)
	Errors    int
}

// ComponentResolver lets processors resolve text mentions to IDs.
type ComponentResolver interface {
	Resolve(ctx context.Context, dataType DataType, mention string) (string, bool, error)
}
