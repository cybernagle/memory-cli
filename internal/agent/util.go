package agent

import (
	"strings"
	"time"
)

func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
