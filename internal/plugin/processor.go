package plugin

import "context"

// Processor extracts structured data from raw inbox memories.
type Processor interface {
	// Name is the unique identifier: "fact-processor", "personality-processor", etc.
	Name() string

	// Consumes declares what data types this processor reads.
	Consumes() []DataType

	// Produces declares what data types this processor outputs.
	Produces() []DataType

	// Process runs extraction on a batch of inbox content.
	Process(ctx context.Context, input ProcessInput) (*ProcessOutput, error)
}
