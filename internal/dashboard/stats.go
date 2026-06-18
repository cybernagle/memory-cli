package dashboard

import (
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type StatsResponse struct {
	Total        int            `json:"total"`
	Inbox        int            `json:"inbox"`
	Organized    int            `json:"organized"`
	Processed    int            `json:"processed"`
	Categories   map[string]int `json:"categories"`
	Sources      map[string]int `json:"sources"`
	Tags         map[string]int `json:"tags"`
	Projects     map[string]int `json:"projects"`
	Roles        map[string]int `json:"roles"`
	Phases       map[string]int `json:"phases"`
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
		Projects:   make(map[string]int),
		Roles:      make(map[string]int),
		Phases:     make(map[string]int),
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
		case store.PhaseProcessed:
			resp.Processed++
		}
		// Full phase breakdown for the dashboard's processing-status panel.
		resp.Phases[string(mem.Phase)]++

		resp.Categories[string(mem.Category)]++

		if mem.Source != "" {
			resp.Sources[mem.Source]++
		}

		if mem.Project != "" {
			resp.Projects[mem.Project]++
		}

		// Role distribution: user vs assistant vs legacy (empty). Shows the ingest change's
		// impact — old data has no role, new ingest captures both turns.
		role := mem.Role
		if role == "" {
			role = "unknown"
		}
		resp.Roles[role]++

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
