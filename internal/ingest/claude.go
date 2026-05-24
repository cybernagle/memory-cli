package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type ClaudeAdapter struct {
	Path string
}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), ".claude")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}

	var memories []*store.Memory

	// Collect all .md files from project memory directories
	projectsDir := filepath.Join(a.Path, "projects")
	if dirs, err := findDirs(projectsDir, "memory"); err == nil {
		for _, dir := range dirs {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				content := strings.TrimSpace(string(data))
				if content == "" {
					continue
				}
				chunks := chunkMarkdown(content, e.Name())
				for _, chunk := range chunks {
					tags := []string{"claude", "project-memory"}
					if chunk.tag != "" {
						tags = append(tags, chunk.tag)
					}
					memories = append(memories, &store.Memory{
						Content:  chunk.content,
						Phase:    store.PhaseOrganized,
						Category: store.CategoryKnowledge,
						Scope:    "global",
						Tags:     tags,
						Source:   "claude",
					})
				}
			}
		}
	}

	return memories, nil
}

type mdChunk struct {
	content string
	tag     string
}

// chunkMarkdown splits a markdown file into individual memory chunks.
// - Strips YAML frontmatter (extracts name as tag)
// - Splits by ## section headers
// - Short content without sections stays as one chunk
func chunkMarkdown(content, filename string) []mdChunk {
	body := content
	tag := ""

	// Strip YAML frontmatter
	if strings.HasPrefix(body, "---") {
		end := strings.Index(body[3:], "---")
		if end >= 0 {
			fm := body[3 : end+3]
			body = strings.TrimSpace(body[end+6:])
			tag = extractFrontmatterTag(fm)
		}
	}

	if body == "" {
		return nil
	}

	// Split by ## sections
	parts := splitBySections(body)
	if len(parts) <= 1 {
		return []mdChunk{{content: body, tag: tag}}
	}

	var chunks []mdChunk
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Skip chunks that are just a heading with no body
		lines := strings.SplitN(p, "\n", 2)
		if len(lines) == 1 && len(p) < 20 && strings.HasPrefix(p, "#") {
			continue
		}
		chunks = append(chunks, mdChunk{content: p, tag: tag})
	}
	return chunks
}

// splitBySections splits markdown by ## headers, keeping the header with its content.
func splitBySections(body string) []string {
	lines := strings.Split(body, "\n")
	var sections []string
	var current []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && len(current) > 0 {
			sections = append(sections, strings.Join(current, "\n"))
			current = current[:0]
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, strings.Join(current, "\n"))
	}
	return sections
}

// extractFrontmatterTag extracts the name field from YAML frontmatter as a tag.
func extractFrontmatterTag(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "name:"); ok {
			v := strings.TrimSpace(after)
			v = strings.Trim(v, "\"'")
			if v != "" {
				return strings.ToLower(strings.ReplaceAll(v, " ", "-"))
			}
		}
	}
	return ""
}
