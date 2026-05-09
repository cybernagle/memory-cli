package store

import (
	"fmt"
	"strings"
	"time"
)

// Backlink represents a reference from one memory to another.
type Backlink struct {
	SourceID    string    `json:"source_id"`
	SourcePhase Phase     `json:"source_phase"`
	TargetTitle string    `json:"target_title"`
	CreatedAt   time.Time `json:"created_at"`
}

// ResolveBacklinks scans all memories for [[wikilinks]] and updates the Links field
// on target memories to record incoming references.
func (s *Store) ResolveBacklinks() (int, error) {
	all, err := s.List(ListOptions{})
	if err != nil {
		return 0, err
	}

	// Build title → memory index (by content prefix match)
	titleIndex := make(map[string]*Memory)
	for _, mem := range all {
		title := titleFromContent(mem.Content)
		if title != "" {
			titleIndex[strings.ToLower(title)] = mem
		}
	}

	count := 0
	for _, mem := range all {
		wikiLinks := ExtractWikiLinks(mem.Content)
		if len(wikiLinks) == 0 {
			continue
		}

		changed := false
		for _, link := range wikiLinks {
			target, ok := titleIndex[strings.ToLower(link)]
			if !ok || target.ID == mem.ID {
				continue
			}
			// Add source ID to target's Links if not already present
			if !containsString(target.Links, mem.ID) {
				target.Links = append(target.Links, mem.ID)
				if err := s.writeToFile(target); err != nil {
					continue
				}
				changed = true
			}
		}
		if changed {
			count++
		}
	}
	return count, nil
}

// GetBacklinks returns all memories that link to the given memory ID.
func (s *Store) GetBacklinks(id string) ([]*Memory, error) {
	all, err := s.List(ListOptions{})
	if err != nil {
		return nil, err
	}

	var backlinks []*Memory
	for _, mem := range all {
		if containsString(mem.Links, id) {
			backlinks = append(backlinks, mem)
		}
	}
	return backlinks, nil
}

// LinkMemories creates a bidirectional link between two memories.
func (s *Store) LinkMemories(sourceID, targetID string) error {
	source, err := s.findByID(sourceID)
	if err != nil {
		return fmt.Errorf("source not found: %w", err)
	}
	target, err := s.findByID(targetID)
	if err != nil {
		return fmt.Errorf("target not found: %w", err)
	}

	if !containsString(source.Links, targetID) {
		source.Links = append(source.Links, targetID)
		source.UpdatedAt = time.Now()
		if err := s.writeToFile(source); err != nil {
			return err
		}
	}

	if !containsString(target.Links, sourceID) {
		target.Links = append(target.Links, sourceID)
		target.UpdatedAt = time.Now()
		if err := s.writeToFile(target); err != nil {
			return err
		}
	}
	return nil
}

// UnlinkMemories removes the bidirectional link between two memories.
func (s *Store) UnlinkMemories(sourceID, targetID string) error {
	source, err := s.findByID(sourceID)
	if err != nil {
		return err
	}
	target, err := s.findByID(targetID)
	if err != nil {
		return err
	}

	source.Links = removeString(source.Links, targetID)
	source.UpdatedAt = time.Now()
	if err := s.writeToFile(source); err != nil {
		return err
	}

	target.Links = removeString(target.Links, sourceID)
	target.UpdatedAt = time.Now()
	if err := s.writeToFile(target); err != nil {
		return err
	}
	return nil
}

func titleFromContent(content string) string {
	// First line, trimmed, up to 100 chars
	lines := strings.SplitN(content, "\n", 2)
	title := strings.TrimSpace(lines[0])
	if len(title) > 100 {
		title = title[:100]
	}
	return title
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var result []string
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}
