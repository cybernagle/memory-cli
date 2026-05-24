package plugin

// Ingest is a data source adapter that produces inbox items.
type Ingest interface {
	Name() string
	Ingest() ([]InboxItem, error)
}
