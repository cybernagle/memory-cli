package store

// Store defines the interface for memory storage backends.
type Store interface {
	// CRUD
	WriteToInbox(content string, scope string, tags []string, source string, project string) (*Memory, error)
	Write(content string, memType Phase, category Category, scope string, tags []string, source string) (*Memory, error)
	Read(id string) (*Memory, error)
	Delete(id string) error
	List(opts ListOptions) ([]*Memory, error)
	Tag(id string, add, remove []string) (*Memory, error)
	Upgrade(id string) error

	// Lookup
	FindByID(id string) (*Memory, error)
	FindByHash(hash string) (*Memory, error)

	// Search
	Search(opts SearchOptions) ([]*Memory, error)
	SearchLike(opts SearchOptions) ([]*Memory, error)

	// Links
	ResolveBacklinks() (int, error)
	GetBacklinks(id string) ([]*Memory, error)
	LinkMemories(sourceID, targetID string) error
	UnlinkMemories(sourceID, targetID string) error

	// Ingest a pre-built memory (preserves CreatedAt, SessionID, etc.)
	IngestMemory(mem *Memory) error

	// Mark an inbox memory as processed (phase = "processed")
	MarkProcessed(id string) error

	// Mark an inbox memory as consumed by a specific processor
	MarkConsumed(id string, processorName string) error

	// List inbox memories not yet consumed by the given processor
	ListUnconsumed(processorName string) ([]*Memory, error)
}
