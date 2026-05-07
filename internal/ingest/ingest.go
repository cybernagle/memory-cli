package ingest

import (
	"fmt"

	"github.com/cybernagle/memory-cli/internal/store"
)

type Adapter interface {
	Name() string
	Ingest() ([]*store.Memory, error)
}

type IngestError struct {
	Source string
	Err    error
}

func (e *IngestError) Error() string {
	return fmt.Sprintf("ingest %s: %v", e.Source, e.Err)
}
