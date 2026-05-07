package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/cybernagle/memory-cli/internal/config"
)

const frontMatterSeparator = "---"

type Store struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Store {
	return &Store{cfg: cfg}
}

func (s *Store) Init() error {
	for _, dir := range []string{s.cfg.ShortTermDir(), s.cfg.LongTermDir()} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}

func (s *Store) dirForType(t MemoryType) string {
	switch t {
	case ShortTerm:
		return s.cfg.ShortTermDir()
	case LongTerm:
		return s.cfg.LongTermDir()
	default:
		return s.cfg.LongTermDir()
	}
}

func (s *Store) Write(content string, memType MemoryType, scope string, tags []string, source string) (*Memory, error) {
	now := time.Now()
	mem := &Memory{
		ID:        uuid.New().String()[:8],
		Content:   content,
		Type:      memType,
		Scope:     defaultString(scope, "global"),
		Tags:      tags,
		Source:    defaultString(source, "manual"),
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
	if memType == ShortTerm {
		ttl, _ := time.ParseDuration(s.cfg.Storage.ShortTermTTL)
		expires := now.Add(ttl)
		mem.ExpiresAt = &expires
	}
	if err := s.writeToFile(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *Store) Read(id string) (*Memory, error) {
	mem, err := s.findByID(id)
	if err != nil {
		return nil, err
	}
	mem.AccessCount++
	mem.UpdatedAt = time.Now()
	if err := s.writeToFile(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *Store) Delete(id string) error {
	mem, err := s.findByID(id)
	if err != nil {
		return err
	}
	path := s.filePath(mem)
	return os.Remove(path)
}

type ListOptions struct {
	Type   MemoryType
	Scope  string
	Source string
	Limit  int
}

func (s *Store) List(opts ListOptions) ([]*Memory, error) {
	var memories []*Memory

	dirs := []string{s.cfg.ShortTermDir(), s.cfg.LongTermDir()}
	if opts.Type != "" {
		dirs = []string{s.dirForType(opts.Type)}
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read dir %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			mem, err := s.readFromFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			if opts.Scope != "" && mem.Scope != opts.Scope {
				continue
			}
			if opts.Source != "" && mem.Source != opts.Source {
				continue
			}
			memories = append(memories, mem)
		}
	}

	if opts.Limit > 0 && len(memories) > opts.Limit {
		memories = memories[:opts.Limit]
	}
	return memories, nil
}

func (s *Store) Tag(id string, add, remove []string) (*Memory, error) {
	mem, err := s.findByID(id)
	if err != nil {
		return nil, err
	}
	tagSet := make(map[string]bool)
	for _, t := range mem.Tags {
		tagSet[t] = true
	}
	for _, t := range add {
		tagSet[t] = true
	}
	for _, t := range remove {
		delete(tagSet, t)
	}
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	mem.Tags = tags
	mem.UpdatedAt = time.Now()
	if err := s.writeToFile(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *Store) Upgrade(id string) error {
	mem, err := s.findByID(id)
	if err != nil {
		return err
	}
	if mem.Type == LongTerm {
		return nil
	}
	oldPath := s.filePath(mem)
	mem.Type = LongTerm
	mem.ExpiresAt = nil
	mem.UpdatedAt = time.Now()
	if err := s.writeToFile(mem); err != nil {
		return err
	}
	return os.Remove(oldPath)
}

func (s *Store) findByID(id string) (*Memory, error) {
	dirs := []string{s.cfg.ShortTermDir(), s.cfg.LongTermDir()}
	for _, dir := range dirs {
		path := filepath.Join(dir, id+".md")
		if _, err := os.Stat(path); err == nil {
			return s.readFromFile(path)
		}
	}
	return nil, fmt.Errorf("memory not found: %s", id)
}

func (s *Store) filePath(mem *Memory) string {
	return filepath.Join(s.dirForType(mem.Type), mem.ID+".md")
}

type frontmatter struct {
	ID          string     `yaml:"id"`
	Type        MemoryType `yaml:"type"`
	Scope       string     `yaml:"scope"`
	Tags        []string   `yaml:"tags,omitempty"`
	Source      string     `yaml:"source"`
	CreatedAt   time.Time  `yaml:"created_at"`
	UpdatedAt   time.Time  `yaml:"updated_at"`
	ExpiresAt   *time.Time `yaml:"expires_at,omitempty"`
	AccessCount int        `yaml:"access_count"`
	Links       []string   `yaml:"links,omitempty"`
	Version     int        `yaml:"version"`
}

func (s *Store) writeToFile(mem *Memory) error {
	dir := s.dirForType(mem.Type)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fm := frontmatter{
		ID:          mem.ID,
		Type:        mem.Type,
		Scope:       mem.Scope,
		Tags:        mem.Tags,
		Source:      mem.Source,
		CreatedAt:   mem.CreatedAt,
		UpdatedAt:   mem.UpdatedAt,
		ExpiresAt:   mem.ExpiresAt,
		AccessCount: mem.AccessCount,
		Links:       mem.Links,
		Version:     mem.Version,
	}

	var sb strings.Builder
	sb.WriteString(frontMatterSeparator + "\n")
	enc := yaml.NewEncoder(&sb)
	if err := enc.Encode(&fm); err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}
	enc.Close()
	sb.WriteString(frontMatterSeparator + "\n")
	sb.WriteString(mem.Content + "\n")

	path := filepath.Join(dir, mem.ID+".md")
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

func (s *Store) readFromFile(path string) (*Memory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if !strings.HasPrefix(content, frontMatterSeparator+"\n") {
		return nil, fmt.Errorf("invalid frontmatter in %s", path)
	}
	end := strings.Index(content[len(frontMatterSeparator)+1:], frontMatterSeparator)
	if end == -1 {
		return nil, fmt.Errorf("unclosed frontmatter in %s", path)
	}
	fm := content[len(frontMatterSeparator)+1 : len(frontMatterSeparator)+1+end]
	body := content[len(frontMatterSeparator)+1+end+len(frontMatterSeparator):]
	body = strings.Trim(body, "\n")

	var mem Memory
	if err := yaml.Unmarshal([]byte(fm), &mem); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	mem.Content = body
	return &mem, nil
}

func defaultString(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
