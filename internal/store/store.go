package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/cybernagle/memory-cli/internal/config"
)

const frontMatterSeparator = "---"

// ErrNotFound is returned when a memory ID does not exist.
var ErrNotFound = errors.New("not found")

// Compile-time check that FileStore implements Store.
var _ Store = (*FileStore)(nil)

type FileStore struct {
	cfg *config.Config
}

func NewFileStore(cfg *config.Config) *FileStore {
	return &FileStore{cfg: cfg}
}

// New is an alias for NewFileStore for backward compatibility.
func New(cfg *config.Config) *FileStore {
	return NewFileStore(cfg)
}

func (s *FileStore) Init() error {
	dirs := []string{s.cfg.ShortTermDir(), s.cfg.LongTermDir()}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	for _, cat := range append(append(AllCategories, CategoryInbox), CategoryReminders) {
		dir := s.cfg.CategoryDir(string(cat))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create category dir %s: %w", dir, err)
		}
	}
	return nil
}

func (s *FileStore) WriteToInbox(content string, scope string, tags []string, source string) (*Memory, error) {
	now := time.Now()
	mem := &Memory{
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: HashContent(content),
		Phase:       PhaseInbox,
		Category:    CategoryInbox,
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		CreatedAt:   now,
		UpdatedAt:   now,
		Version:     1,
		Links:       ExtractWikiLinks(content),
	}
	ttl, err := parseDuration(s.cfg.Storage.ShortTermTTL)
	if err != nil {
		ttl = 168 * time.Hour
	}
	expires := now.Add(ttl)
	mem.ExpiresAt = &expires

	if err := s.writeToFile(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *FileStore) Write(content string, memType Phase, category Category, scope string, tags []string, source string) (*Memory, error) {
	now := time.Now()
	mem := &Memory{
		ID:          uuid.New().String(),
		Content:     content,
		ContentHash: HashContent(content),
		Phase:       memType,
		Category:    category,
		Scope:       defaultString(scope, "global"),
		Tags:        tags,
		Source:      defaultString(source, "manual"),
		CreatedAt:   now,
		UpdatedAt:   now,
		Version:     1,
		Links:       ExtractWikiLinks(content),
	}
	if memType == PhaseInbox {
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

func (s *FileStore) Read(id string) (*Memory, error) {
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

func (s *FileStore) Delete(id string) error {
	mem, err := s.findByID(id)
	if err != nil {
		return err
	}
	path := s.filePath(mem)
	return os.Remove(path)
}

type ListOptions struct {
	Phase         Phase
	Category      Category
	Scope         string
	Source        string
	SessionID     string
	Limit         int
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Tags          []string
}

func (s *FileStore) List(opts ListOptions) ([]*Memory, error) {
	var memories []*Memory

	dirs := s.listDirs(opts)

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
			if opts.Phase != "" && mem.Phase != opts.Phase {
				if opts.CreatedAfter != nil && mem.CreatedAt.Before(*opts.CreatedAfter) {
					continue
				}
				if opts.CreatedBefore != nil && !mem.CreatedAt.Before(*opts.CreatedBefore) {
					continue
				}
				if len(opts.Tags) > 0 && !hasAllTags(mem.Tags, opts.Tags) {
					continue
				}
				continue
			}
			memories = append(memories, mem)
		}
	}

	sort.Slice(memories, func(i, j int) bool {
		return memories[i].CreatedAt.After(memories[j].CreatedAt)
	})

	if opts.Limit > 0 && len(memories) > opts.Limit {
		memories = memories[:opts.Limit]
	}
	return memories, nil
}

func (s *FileStore) listDirs(opts ListOptions) []string {
	if opts.Category != "" {
		dir := s.cfg.CategoryDir(string(opts.Category))
		return []string{dir}
	}
	dirs := []string{s.cfg.ShortTermDir(), s.cfg.LongTermDir()}
	for _, cat := range append(append(AllCategories, CategoryInbox), CategoryReminders) {
		dirs = append(dirs, s.cfg.CategoryDir(string(cat)))
	}
	return dirs
}

func (s *FileStore) Tag(id string, add, remove []string) (*Memory, error) {
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
	sort.Strings(tags)
	mem.Tags = tags
	mem.UpdatedAt = time.Now()
	if err := s.writeToFile(mem); err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *FileStore) Upgrade(id string) error {
	mem, err := s.findByID(id)
	if err != nil {
		return err
	}
	if mem.Phase == PhaseOrganized {
		return nil
	}
	oldPath := s.filePath(mem)
	mem.Phase = PhaseOrganized
	mem.ExpiresAt = nil
	mem.UpdatedAt = time.Now()
	newDir := s.cfg.CategoryDir(string(mem.Category))
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

func (s *FileStore) findByID(id string) (*Memory, error) {
	dirs := s.listDirs(ListOptions{})
	for _, dir := range dirs {
		path := filepath.Join(dir, id+".md")
		if _, err := os.Stat(path); err == nil {
			return s.readFromFile(path)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// FindByID returns a memory by ID without incrementing access count.
func (s *FileStore) FindByID(id string) (*Memory, error) {
	return s.findByID(id)
}

func (s *FileStore) filePath(mem *Memory) string {
	if mem.Category != "" && mem.Category != CategoryInbox {
		return filepath.Join(s.cfg.CategoryDir(string(mem.Category)), mem.ID+".md")
	}
	return filepath.Join(s.dirForPhase(mem.Phase), mem.ID+".md")
}

func (s *FileStore) dirForPhase(p Phase) string {
	switch p {
	case PhaseInbox:
		return s.cfg.InboxDir()
	default:
		return s.cfg.LongTermDir()
	}
}

type frontmatter struct {
	ID          string     `yaml:"id"`
	ContentHash string     `yaml:"content_hash"`
	Phase       Phase      `yaml:"phase"`
	Category    Category   `yaml:"category"`
	Scope       string     `yaml:"scope"`
	Tags        []string   `yaml:"tags,omitempty"`
	Source      string     `yaml:"source"`
	CreatedAt   time.Time  `yaml:"created_at"`
	UpdatedAt   time.Time  `yaml:"updated_at"`
	ExpiresAt   *time.Time `yaml:"expires_at,omitempty"`
	AccessCount int        `yaml:"access_count"`
	Links       []string   `yaml:"links,omitempty"`
	Version     int        `yaml:"version"`
	// Legacy fields for backward compat
	Type string `yaml:"type,omitempty"`
}

func (s *FileStore) writeToFile(mem *Memory) error {
	dir := s.fileDir(mem)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fm := frontmatter{
		ID:          mem.ID,
		ContentHash: mem.ContentHash,
		Phase:       mem.Phase,
		Category:    mem.Category,
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
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(sb.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *FileStore) fileDir(mem *Memory) string {
	if mem.Category != "" {
		return s.cfg.CategoryDir(string(mem.Category))
	}
	return s.dirForPhase(mem.Phase)
}

func (s *FileStore) readFromFile(path string) (*Memory, error) {
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
	fmData := rest[:end]
	body := rest[end+len("\n"+frontMatterSeparator+"\n"):]
	body = strings.Trim(body, "\n")

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmData), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Backward compat: legacy "type" field → Phase
	phase := fm.Phase
	if phase == "" {
		switch fm.Type {
		case "short":
			phase = PhaseInbox
		case "long":
			phase = PhaseOrganized
		default:
			phase = PhaseOrganized
		}
	}

	mem := &Memory{
		ID:          fm.ID,
		Content:     body,
		ContentHash: fm.ContentHash,
		Phase:       phase,
		Category:    fm.Category,
		Scope:       fm.Scope,
		Tags:        fm.Tags,
		Source:      fm.Source,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
		ExpiresAt:   fm.ExpiresAt,
		AccessCount: fm.AccessCount,
		Links:       fm.Links,
		Version:     fm.Version,
	}
	return mem, nil
}

func defaultString(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

func HashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

func (s *FileStore) FindByHash(hash string) (*Memory, error) {
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

func ExtractWikiLinks(content string) []string {
	return extractWikiLinks(content)
}

func extractWikiLinks(content string) []string {
	var links []string
	seen := make(map[string]bool)
	i := 0
	for i < len(content)-1 {
		if content[i] == '[' && content[i+1] == '[' {
			end := strings.Index(content[i+2:], "]]")
			if end != -1 {
				link := strings.TrimSpace(content[i+2 : i+2+end])
				if link != "" && !seen[link] {
					seen[link] = true
					links = append(links, link)
				}
				i = i + 2 + end + 2
				continue
			}
		}
		i++
	}
	return links
}
