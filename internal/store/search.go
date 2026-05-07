package store

import (
	"strings"
	"time"
)

type SearchOptions struct {
	Query string
	Tags  []string
	Scope string
	Type  MemoryType
	From  *time.Time
	To    *time.Time
}

func (s *Store) Search(opts SearchOptions) ([]*Memory, error) {
	all, err := s.List(ListOptions{Type: opts.Type, Scope: opts.Scope})
	if err != nil {
		return nil, err
	}

	queryLower := strings.ToLower(opts.Query)
	var results []*Memory

	for _, mem := range all {
		if opts.Query != "" && !strings.Contains(strings.ToLower(mem.Content), queryLower) {
			continue
		}
		if len(opts.Tags) > 0 && !hasAllTags(mem.Tags, opts.Tags) {
			continue
		}
		if opts.From != nil && mem.CreatedAt.Before(*opts.From) {
			continue
		}
		if opts.To != nil && mem.CreatedAt.After(*opts.To) {
			continue
		}
		results = append(results, mem)
	}
	return results, nil
}

func hasAllTags(memoryTags, requiredTags []string) bool {
	tagSet := make(map[string]bool)
	for _, t := range memoryTags {
		tagSet[strings.ToLower(t)] = true
	}
	for _, t := range requiredTags {
		if !tagSet[strings.ToLower(t)] {
			return false
		}
	}
	return true
}
