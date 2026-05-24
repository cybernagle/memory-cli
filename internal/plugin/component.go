package plugin

import (
	"context"
	"database/sql"
)

// Component manages a data type (Entity, Relation, Event, Rule, Context).
// Each component owns its SQLite tables and lifecycle.
type Component interface {
	// Name is the unique identifier: "entity", "relation", "event", etc.
	Name() string

	// DataType returns what kind of data this component produces.
	DataType() DataType

	// Schema returns SQL statements to create this component's tables.
	Schema() []string

	// Init gives the component a *sql.DB. Execute Schema() here.
	Init(ctx context.Context, db *sql.DB) error

	// DecayPolicy returns how data of this type should decay.
	DecayPolicy() DecayPolicy

	// Resolve takes a text mention and returns the canonical ID.
	Resolve(ctx context.Context, mention string) (id string, ok bool, err error)

	// Display returns a human-readable string for a given ID.
	Display(ctx context.Context, id string) (string, error)
}
