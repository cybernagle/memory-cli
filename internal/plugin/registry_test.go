package plugin

import (
	"context"
	"database/sql"
	"testing"
)

type mockComponent struct{ name string }

func (m *mockComponent) Name() string                                                         { return m.name }
func (m *mockComponent) DataType() DataType                                                   { return DataEntity }
func (m *mockComponent) Schema() []string                                                      { return nil }
func (m *mockComponent) Init(_ context.Context, _ *sql.DB) error                              { return nil }
func (m *mockComponent) DecayPolicy() DecayPolicy                                              { return DecayPolicy{Strategy: "none"} }
func (m *mockComponent) Resolve(_ context.Context, _ string) (string, bool, error)             { return "", false, nil }
func (m *mockComponent) Display(_ context.Context, _ string) (string, error)                   { return "", nil }

type mockProcessor struct{ name string }

func (m *mockProcessor) Name() string                                                         { return m.name }
func (m *mockProcessor) Consumes() []DataType                                                 { return nil }
func (m *mockProcessor) Produces() []DataType                                                 { return nil }
func (m *mockProcessor) Process(_ context.Context, _ ProcessInput) (*ProcessOutput, error)     {
	return &ProcessOutput{}, nil
}

func TestRegistryComponent(t *testing.T) {
	r := NewRegistry()
	r.RegisterComponent(&mockComponent{name: "entity"})

	c, ok := r.Component("entity")
	if !ok {
		t.Fatal("expected to find entity component")
	}
	if c.Name() != "entity" {
		t.Fatalf("expected entity, got %s", c.Name())
	}

	all := r.AllComponents()
	if len(all) != 1 {
		t.Fatalf("expected 1 component, got %d", len(all))
	}
}

func TestRegistryProcessor(t *testing.T) {
	r := NewRegistry()
	r.RegisterProcessor(&mockProcessor{name: "fact"})

	p, ok := r.Processor("fact")
	if !ok {
		t.Fatal("expected to find fact processor")
	}
	if p.Name() != "fact" {
		t.Fatalf("expected fact, got %s", p.Name())
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.RegisterComponent(&mockComponent{name: "entity"})

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.RegisterComponent(&mockComponent{name: "entity"})
}

func TestRegistryNotFound(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Component("nonexistent")
	if ok {
		t.Fatal("expected not found")
	}
}
