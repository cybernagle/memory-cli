package config

import (
	"fmt"
	"os"
	"path/filepath"

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
}

type DaemonConfig struct {
	Interval string `yaml:"interval"`
}

type SourceConfig struct {
	Path    string `yaml:"path"`
	Enabled bool   `yaml:"enabled"`
}

type IngestionConfig struct {
	Logseq   SourceConfig `yaml:"logseq"`
	Obsidian SourceConfig `yaml:"obsidian"`
}

type Config struct {
	Storage   StorageConfig   `yaml:"storage"`
	Daemon    DaemonConfig    `yaml:"daemon"`
	Ingestion IngestionConfig `yaml:"ingestion"`
}

func DefaultConfig() *Config {
	home := MustHomeDir()
	return &Config{
		Storage: StorageConfig{
			Root:         filepath.Join(home, ".memory"),
			ShortTermTTL: "24h",
		},
		Daemon: DaemonConfig{
			Interval: "60s",
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

func (c *Config) ShortTermDir() string {
	return filepath.Join(c.Storage.Root, "short-term")
}

func (c *Config) LongTermDir() string {
	return filepath.Join(c.Storage.Root, "long-term")
}
