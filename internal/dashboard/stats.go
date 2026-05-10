package dashboard

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type StatsResponse struct {
	Total        int            `json:"total"`
	Inbox        int            `json:"inbox"`
	Organized    int            `json:"organized"`
	Categories   map[string]int `json:"categories"`
	Sources      map[string]int `json:"sources"`
	Tags         map[string]int `json:"tags"`
	Recent24h    int            `json:"recent_24h"`
	ExpiringSoon int            `json:"expiring_soon"`
}

type statser interface {
	List(opts store.ListOptions) ([]*store.Memory, error)
}

func ComputeStats(s statser) (*StatsResponse, error) {
	all, err := s.List(store.ListOptions{})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	dayAgo := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)

	resp := &StatsResponse{
		Total:      len(all),
		Categories: make(map[string]int),
		Sources:    make(map[string]int),
		Tags:       make(map[string]int),
	}

	for _, cat := range store.AllCategories {
		resp.Categories[string(cat)] = 0
	}
	resp.Categories["inbox"] = 0
	resp.Categories["reminders"] = 0

	for _, mem := range all {
		switch mem.Phase {
		case store.PhaseInbox:
			resp.Inbox++
		case store.PhaseOrganized:
			resp.Organized++
		}

		resp.Categories[string(mem.Category)]++

		if mem.Source != "" {
			resp.Sources[mem.Source]++
		}

		for _, tag := range mem.Tags {
			resp.Tags[tag]++
		}

		if mem.CreatedAt.After(dayAgo) {
			resp.Recent24h++
		}

		if mem.ExpiresAt != nil && !mem.ExpiresAt.IsZero() && mem.ExpiresAt.After(now) && mem.ExpiresAt.Before(tomorrow) {
			resp.ExpiringSoon++
		}
	}

	return resp, nil
}
