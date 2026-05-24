package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cybernagle/memory-cli/internal/store"
	"gopkg.in/yaml.v3"
)

type ObsidianAdapter struct {
	Path string
}

func (a *ObsidianAdapter) Name() string { return "obsidian" }

func (a *ObsidianAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		return nil, nil
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}
	var memories []*store.Memory

	err := filepath.Walk(a.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".obsidian" || info.Name() == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		body, tags := parseObsidianFile(content)
		body = strings.TrimSpace(body)
		if body == "" {
			return nil
		}

		relPath, _ := filepath.Rel(a.Path, path)
		dirTag := filepath.Dir(relPath)
		if dirTag == "." {
			dirTag = ""
		}
		allTags := []string{"obsidian"}
		if dirTag != "" {
			allTags = append(allTags, strings.ReplaceAll(dirTag, "/", "-"))
		}
		allTags = append(allTags, tags...)

		memories = append(memories, &store.Memory{
			Content:  truncate(body, 10000),
			Phase:    store.PhaseInbox,
			Category: store.CategoryKnowledge,
			Scope:    "global",
			Tags:     uniqueTags(allTags),
			Source:   "obsidian",
		})
		return nil
	})

	return memories, err
}

func parseObsidianFile(content string) (string, []string) {
	var tags []string
	if !strings.HasPrefix(content, "---\n") {
		return extractInlineTags(content), nil
	}
	end := strings.Index(content[4:], "---\n")
	if end == -1 {
		return extractInlineTags(content), nil
	}
	fm := content[4 : 4+end]
	body := content[4+end+4:]

	var frontmatter struct {
		Tags    interface{} `yaml:"tags"`
		Aliases interface{} `yaml:"aliases"`
	}
	if err := yaml.Unmarshal([]byte(fm), &frontmatter); err == nil {
		tags = append(tags, extractYamlTags(frontmatter.Tags)...)
		tags = append(tags, extractYamlTags(frontmatter.Aliases)...)
	}

	return body, tags
}

func extractYamlTags(val interface{}) []string {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []interface{}:
		var tags []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				tags = append(tags, strings.ToLower(strings.TrimSpace(s)))
			}
		}
		return tags
	case string:
		return []string{strings.ToLower(strings.TrimSpace(v))}
	}
	return nil
}

func extractInlineTags(content string) string {
	return content
}

func uniqueTags(tags []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	return result
}
