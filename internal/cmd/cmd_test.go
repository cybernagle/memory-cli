package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestCmd(t *testing.T) func() {
	t.Helper()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("storage:\n  root: "+tmpDir+"\n  short_term_ttl: 24h\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCfgPath := cfgPath
	cfgPath = cfgFile

	// Reset flags from previous tests
	writeCategory = ""
	writeScope = "global"
	writeSource = "manual"
	writeTags = ""
	listCategory = ""
	listScope = ""
	listSource = ""
	listLimit = 50

	return func() {
		cfgPath = origCfgPath
	}
}

func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// extractIDFromOutput extracts a UUID from output like "Created inbox memory memory: abc123-..."
func extractIDFromOutput(output string) string {
	parts := strings.Split(output, "memory: ")
	if len(parts) < 2 {
		return ""
	}
	id := strings.TrimSpace(strings.Split(parts[len(parts)-1], "\n")[0])
	if len(id) >= 32 && strings.Contains(id, "-") {
		return id
	}
	return ""
}

func extractInboxIDFromOutput(output string) string {
	parts := strings.Split(output, "inbox memory: ")
	if len(parts) < 2 {
		return ""
	}
	id := strings.TrimSpace(strings.Split(parts[len(parts)-1], "\n")[0])
	if len(id) >= 32 && strings.Contains(id, "-") {
		return id
	}
	return ""
}

func TestWriteAndRead(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"write", "hello world", "--category", "knowledge"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "inbox memory") {
		t.Fatalf("unexpected write output: %s", output)
	}

	id := extractIDFromOutput(output)
	if id == "" {
		t.Fatalf("could not extract ID from write output: %s", output)
	}

	output = captureOutput(t, func() {
		rootCmd.SetArgs([]string{"read", id})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "hello world") {
		t.Fatalf("read output missing content:\n%s", output)
	}
}

func TestWriteInvalidCategory(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	rootCmd.SetArgs([]string{"write", "test", "--category", "invalid"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid category")
	}
}

func TestListEmpty(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"list"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "No memories found") {
		t.Fatalf("unexpected list output: %s", output)
	}
}

func TestSearchNoResults(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"search", "nonexistent"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "No matching memories found") {
		t.Fatalf("unexpected search output: %s", output)
	}
}

func TestWriteInboxAndRead(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"write", "inbox note"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "Created inbox memory") {
		t.Fatalf("unexpected write output: %s", output)
	}

	id := extractInboxIDFromOutput(output)
	if id == "" {
		t.Fatalf("could not extract inbox ID from write output: %s", output)
	}

	output = captureOutput(t, func() {
		rootCmd.SetArgs([]string{"read", id})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "inbox note") {
		t.Fatalf("read output missing content:\n%s", output)
	}
}
