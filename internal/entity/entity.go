package entity

import (
	"context"
	"database/sql"

	"github.com/cybernagle/memory-cli/internal/plugin"
)

// EntityComponent implements plugin.Component for the Entity data type.
type EntityComponent struct {
	store *EntityStore
}

func NewEntityComponent() *EntityComponent {
	return &EntityComponent{}
}

func (c *EntityComponent) Name() string {
	return "entity"
}

func (c *EntityComponent) DataType() plugin.DataType {
	return plugin.DataEntity
}

func (c *EntityComponent) Schema() []string {
	return []string{entitySchema}
}

func (c *EntityComponent) Init(ctx context.Context, db *sql.DB) error {
	c.store = NewEntityStore(db)
	_, err := db.Exec(entitySchema)
	return err
}

func (c *EntityComponent) DecayPolicy() plugin.DecayPolicy {
	return plugin.DecayPolicy{
		Strategy:   "access-count",
		NeverPurge: true,
	}
}

func (c *EntityComponent) Resolve(ctx context.Context, mention string) (string, bool, error) {
	if c.store == nil {
		return "", false, nil
	}
	return c.store.Resolve(ctx, mention)
}

func (c *EntityComponent) Display(ctx context.Context, id string) (string, error) {
	if c.store == nil {
		return "", nil
	}
	return c.store.DisplayName(ctx, id)
}

// Store exposes the underlying EntityStore for direct use by the pipeline.
func (c *EntityComponent) Store() *EntityStore {
	return c.store
}
