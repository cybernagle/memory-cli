package store

import (
	"strings"
	"testing"
)

func TestCategorizeByKeywordsRules(t *testing.T) {
	cases := []struct {
		content string
		want    Category
	}{
		{"I prefer dark mode for the editor", CategoryPreferences},
		{"i dislike the default font", CategoryPreferences},
		{"we decided to use react for the frontend", CategoryDecisions},
		{"we must standardize on go modules", CategoryDecisions},
		{"learned that goroutines can leak if not tracked", CategoryLessons},
		{"turns out the api was rate limiting us", CategoryLessons},
		{"remind me to deploy before friday", CategoryReminders},
		{"don't forget to renew the cert", CategoryReminders},
		{"how to debug a goroutine leak", CategorySkills},
		{"the api returns json over http", CategoryInbox}, // no cue → inbox at the keyword-rule level
	}
	for _, c := range cases {
		got := CategorizeByKeywords(c.content)
		if got != c.want {
			t.Errorf("CategorizeByKeywords(%q) = %q, want %q", c.content, got, c.want)
		}
	}
}

func TestCategorizeContentFallback(t *testing.T) {
	// technical content with no specific cue falls back to knowledge
	if got := CategorizeContent("the react component renders a list with golang backend"); got != CategoryKnowledge {
		t.Errorf("technical content → %q, want knowledge", got)
	}
	// specific cue still wins over the knowledge fallback
	if got := CategorizeContent("I prefer react over vue for new projects"); got != CategoryPreferences {
		t.Errorf("preference cue → %q, want preferences", got)
	}
	// truly signal-less content stays inbox
	if got := CategorizeContent("好的那就这样吧"); got != CategoryInbox {
		t.Errorf("signal-less content → %q, want inbox", got)
	}
}

func TestNormalizeCategoryRejectsGarbage(t *testing.T) {
	cases := []Category{
		Category("car-agent]]-[[claude-worktrees-session"),
		Category("a-r-kit--a-r-recorder--map-logger--occupancy"), // > 40 chars
		Category(strings.Repeat("x", 100)),                       // absurdly long
	}
	for _, c := range cases {
		if got := NormalizeCategory(c); got != CategoryInbox {
			t.Errorf("NormalizeCategory(%q) = %q, want inbox (garbage rejected)", string(c), got)
		}
	}
}

func TestNormalizeCategoryKeepsLegit(t *testing.T) {
	cases := []struct {
		in, want Category
	}{
		{"car-agent", "car-agent"},     // legit project name preserved
		{"knowledge", "knowledge"},     // standard category
		{"preference", "preferences"},  // alias mapped
		{"Preferences", "preferences"}, // case + alias
	}
	for _, c := range cases {
		if got := NormalizeCategory(c.in); got != c.want {
			t.Errorf("NormalizeCategory(%q) = %q, want %q", string(c.in), got, c.want)
		}
	}
}
