package entity

import (
	"context"
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
)

// EntityResolver resolves [[wiki-link]] text mentions to entity IDs.
type EntityResolver struct {
	component *EntityComponent
}

func NewResolver(component *EntityComponent) *EntityResolver {
	return &EntityResolver{component: component}
}

// ResolveMentions scans content for [[wiki-links]] and resolves each to an entity ID.
// Returns a map of mention text → entity ID. Auto-creates entities for new mentions.
func (r *EntityResolver) ResolveMentions(ctx context.Context, content string, memoryID string) (map[string]string, error) {
	links := store.ExtractWikiLinks(content)
	if len(links) == 0 {
		return nil, nil
	}

	resolved := make(map[string]string)
	for _, link := range links {
		id, ok, err := r.component.Resolve(ctx, link)
		if err != nil {
			continue
		}
		if !ok {
			e, err := r.component.Store().CreateEntity(ctx, strings.ToLower(link), "concept")
			if err != nil {
				continue
			}
			id = e.ID
		}
		resolved[link] = id
		r.component.Store().RecordMention(ctx, id, memoryID, link)
	}

	return resolved, nil
}
