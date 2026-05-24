package plugin

import "fmt"

// Registry is the central registration point for all plugins.
type Registry struct {
	components map[string]Component
	processors map[string]Processor
	ingests    map[string]Ingest
}

func NewRegistry() *Registry {
	return &Registry{
		components: make(map[string]Component),
		processors: make(map[string]Processor),
		ingests:    make(map[string]Ingest),
	}
}

func (r *Registry) RegisterComponent(c Component) {
	if _, exists := r.components[c.Name()]; exists {
		panic(fmt.Sprintf("component plugin %q already registered", c.Name()))
	}
	r.components[c.Name()] = c
}

func (r *Registry) RegisterProcessor(p Processor) {
	if _, exists := r.processors[p.Name()]; exists {
		panic(fmt.Sprintf("processor plugin %q already registered", p.Name()))
	}
	r.processors[p.Name()] = p
}

func (r *Registry) RegisterIngest(i Ingest) {
	if _, exists := r.ingests[i.Name()]; exists {
		panic(fmt.Sprintf("ingest plugin %q already registered", i.Name()))
	}
	r.ingests[i.Name()] = i
}

func (r *Registry) Component(name string) (Component, bool) {
	c, ok := r.components[name]
	return c, ok
}

func (r *Registry) Processor(name string) (Processor, bool) {
	p, ok := r.processors[name]
	return p, ok
}

func (r *Registry) Ingest(name string) (Ingest, bool) {
	i, ok := r.ingests[name]
	return i, ok
}

func (r *Registry) AllComponents() []Component {
	out := make([]Component, 0, len(r.components))
	for _, c := range r.components {
		out = append(out, c)
	}
	return out
}

func (r *Registry) AllProcessors() []Processor {
	out := make([]Processor, 0, len(r.processors))
	for _, p := range r.processors {
		out = append(out, p)
	}
	return out
}

func (r *Registry) AllIngests() []Ingest {
	out := make([]Ingest, 0, len(r.ingests))
	for _, i := range r.ingests {
		out = append(out, i)
	}
	return out
}
