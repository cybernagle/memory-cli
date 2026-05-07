package store

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: hashContent(content),
		Type:        memType,
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		CreatedAt:   now,
		UpdatedAt:   now,
		Version:     1,
	}
	if memType == ShortTerm {
		ttl, err := parseDuration(s.cfg.Storage.ShortTermTTL)
		if err != nil {
			return nil, fmt.Errorf("invalid short_term_ttl %q: %w", s.cfg.Storage.ShortTermTTL, err)
		}
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
				log.Printf("warning: skipping corrupted file %s: %v", entry.Name(), err)
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
	newDir := s.dirForType(LongTerm)
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return err
	}
	newPath := filepath.Join(newDir, mem.ID+".md")
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	if err := s.writeToFile(mem); err != nil {
		os.Rename(newPath, oldPath)
		return err
	}
	return nil
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
	ContentHash string     `yaml:"content_hash"`
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
		ContentHash: mem.ContentHash,
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
	if err := enc.Close(); err != nil {
		return fmt.Errorf("flush frontmatter: %w", err)
	}
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
	sep := frontMatterSeparator + "\n"
	if !strings.HasPrefix(content, sep) {
		return nil, fmt.Errorf("invalid frontmatter in %s", path)
	}
	rest := content[len(sep):]
	end := strings.Index(rest, "\n"+frontMatterSeparator+"\n")
	if end == -1 {
		return nil, fmt.Errorf("unclosed frontmatter in %s", path)
	}
	fm := rest[:end]
	body := rest[end+len("\n"+frontMatterSeparator+"\n"):]
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

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

func (s *Store) FindByHash(hash string) (*Memory, error) {
	memories, err := s.List(ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, mem := range memories {
		if mem.ContentHash == hash {
			return mem, nil
		}
	}
	return nil, nil
}

func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid day duration: %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
