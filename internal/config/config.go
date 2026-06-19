package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

func MustHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("cannot determine home directory: %v", err))
	}
	return home
}

type StorageConfig struct {
	Root         string `yaml:"root"`
	ShortTermTTL string `yaml:"short_term_ttl"`
	Backend      string `yaml:"backend"`
	SQLitePath   string `yaml:"sqlite_path"`
}

type DaemonConfig struct {
	Interval       string `yaml:"interval"`
	DecayThreshold string `yaml:"decay_threshold"`
	UpgradeAccess  int    `yaml:"upgrade_access_threshold"`
}

type SourceConfig struct {
	Path    string `yaml:"path"`
	Enabled bool   `yaml:"enabled"`
}

type IngestionConfig struct {
	Logseq   SourceConfig `yaml:"logseq"`
	Obsidian SourceConfig `yaml:"obsidian"`
}

type NotificationConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Method          string `yaml:"method"` // "osascript" | "file" | "webhook"
	DingDingWebhook string `yaml:"dingding_webhook"`
	DingDingSecret  string `yaml:"dingding_secret"`
	WeChatWebhook   string `yaml:"wechat_webhook"`
}

type APIConfig struct {
	Enabled bool     `yaml:"enabled"`
	Keys    []string `yaml:"keys"`
	Listen  string   `yaml:"listen"`
}

type PipelineConfig struct {
	Enabled   bool `yaml:"enabled"`
	Threshold int  `yaml:"threshold"`
}

type Config struct {
	Storage      StorageConfig      `yaml:"storage"`
	Daemon       DaemonConfig       `yaml:"daemon"`
	Ingestion    IngestionConfig    `yaml:"ingestion"`
	Timezone     string             `yaml:"timezone"`
	Notification NotificationConfig `yaml:"notification"`
	API          APIConfig          `yaml:"api"`
	Pipeline     PipelineConfig     `yaml:"pipeline"`
	Search       SearchConfig       `yaml:"search"`
}

// SearchConfig controls which search/ranking strategy the store uses.
// "idf" (default): custom IDF + CJK prefix matching + first-keyword boost (tuned for Chinese).
// "bm25": FTS5 built-in BM25 ranking (better for English/segmented text).
// "hybrid": BM25 + vector embedding fusion (future, not yet implemented).
type SearchConfig struct {
	Strategy string `yaml:"strategy"`
}

func DefaultConfig() *Config {
	home := MustHomeDir()
	return &Config{
		Storage: StorageConfig{
			Root:         filepath.Join(home, ".memory"),
			ShortTermTTL: "168h", // inbox TTL: 7 days
		},
		Daemon: DaemonConfig{
			Interval:       "60s",
			DecayThreshold: "720h",
			UpgradeAccess:  3,
		},
		Notification: NotificationConfig{
			Enabled: true,
			Method:  "osascript",
		},
		Search: SearchConfig{
			Strategy: "idf",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Location() *time.Location {
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

func (c *Config) ShortTermDir() string {
	return filepath.Join(c.Storage.Root, "short-term")
}

func (c *Config) LongTermDir() string {
	return filepath.Join(c.Storage.Root, "long-term")
}

func (c *Config) CategoriesDir() string {
	return filepath.Join(c.Storage.Root, "categories")
}

func (c *Config) CategoryDir(cat string) string {
	return filepath.Join(c.CategoriesDir(), cat)
}

func (c *Config) InboxDir() string {
	return c.CategoryDir("inbox")
}

func (c *Config) RemindersDir() string {
	return c.CategoryDir("reminders")
}

func (c *Config) MemoryIndexPath() string {
	return filepath.Join(c.Storage.Root, "memory.md")
}

func (c *Config) PendingPath() string {
	return filepath.Join(c.Storage.Root, "pending.md")
}

func SQLiteDefaultPath() string {
	return filepath.Join(MustHomeDir(), ".memory", "memories.db")
}
