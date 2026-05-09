package ingest

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

type LogseqAdapter struct {
	Path string
}

func (a *LogseqAdapter) Name() string { return "logseq" }

var wikiLinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

func (a *LogseqAdapter) Ingest() ([]*store.Memory, error) {
	if a.Path == "" {
		a.Path = filepath.Join(config.MustHomeDir(), "logseq")
	}
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return nil, nil
	}
	var memories []*store.Memory

	pagesDir := filepath.Join(a.Path, "pages")
	entries, err := os.ReadDir(pagesDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("warning: read logseq pages dir: %v", err)
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(pagesDir, e.Name()))
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			pageName := strings.TrimSuffix(e.Name(), ".md")
			tags := []string{"logseq", pageName}
			links := extractWikiLinks(content)

			memories = append(memories, &store.Memory{
				Content:  content,
				Phase:    store.PhaseOrganized,
				Category: store.CategoryKnowledge,
				Scope:    "global",
				Tags:     uniqueTags(tags),
				Links:    links,
				Source:   "logseq",
			})
		}
	}

	journalsDir := filepath.Join(a.Path, "journals")
	entries, err = os.ReadDir(journalsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("warning: read logseq journals dir: %v", err)
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(journalsDir, e.Name()))
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			dateStr := strings.TrimSuffix(e.Name(), ".md")
			tags := []string{"logseq", "journal", dateStr}

			memories = append(memories, &store.Memory{
				Content:  content,
				Phase:    store.PhaseInbox,
				Category: store.CategoryInbox,
				Scope:    "global",
				Tags:     tags,
				Source:   "logseq",
			})
		}
	}

	return memories, nil
}

func extractWikiLinks(content string) []string {
	matches := wikiLinkRe.FindAllStringSubmatch(content, -1)
	var links []string
	for _, m := range matches {
		if len(m) > 1 {
			links = append(links, strings.ToLower(m[1]))
		}
	}
	return links
}
