package plugin

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/ingest"
	"github.com/cybernagle/memory-cli/internal/store"
)

type IngestAdapter struct {
	inner ingest.Adapter
}

func NewIngestAdapter(a ingest.Adapter) *IngestAdapter {
	return &IngestAdapter{inner: a}
}

func (a *IngestAdapter) Name() string { return a.inner.Name() }

func (a *IngestAdapter) Ingest() ([]InboxItem, error) {
	memories, err := a.inner.Ingest()
	if err != nil {
		return nil, err
	}
	items := make([]InboxItem, len(memories))
	for i, mem := range memories {
		items[i] = memoryToInboxItem(mem)
	}
	return items, nil
}

func memoryToInboxItem(m *store.Memory) InboxItem {
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return InboxItem{
		ID:        m.ID,
		Content:   m.Content,
		SessionID: m.Source,
		Source:    m.Source,
		Tags:      m.Tags,
		CreatedAt: createdAt,
	}
}
