package ingest

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

func findFiles(root, suffix string) ([]string, error) {
	var results []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("warning: walk error at %s: %v", path, err)
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), suffix) {
			results = append(results, path)
		}
		return nil
	})
	return results, err
}

func findDirs(root, name string) ([]string, error) {
	var results []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("warning: walk error at %s: %v", path, err)
			return nil
		}
		if info.IsDir() && info.Name() == name {
			results = append(results, path)
		}
		return nil
	})
	return results, err
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
