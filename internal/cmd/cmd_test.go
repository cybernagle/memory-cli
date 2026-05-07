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

func extractIDFromList(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && strings.Contains(fields[0], "-") && (fields[1] == "short" || fields[1] == "long") {
			return fields[0]
		}
	}
	return ""
}

func TestWriteAndRead(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"write", "hello world", "--type", "long"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "Created long memory:") {
		t.Fatalf("unexpected write output: %s", output)
	}

	output = captureOutput(t, func() {
		rootCmd.SetArgs([]string{"list"})
		rootCmd.Execute()
	})

	id := extractIDFromList(output)
	if id == "" {
		t.Fatalf("could not find memory ID in list output:\n%s", output)
	}

	output = captureOutput(t, func() {
		rootCmd.SetArgs([]string{"read", id})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "hello world") {
		t.Fatalf("read output missing content:\n%s", output)
	}
}

func TestWriteInvalidType(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	rootCmd.SetArgs([]string{"write", "test", "--type", "invalid"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid type")
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

func TestSearchInvalidType(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	rootCmd.SetArgs([]string{"search", "test", "--type", "bad"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid search type")
	}
}

func TestListTypeFilter(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	rootCmd.SetArgs([]string{"list", "--type", "invalid"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid list type filter")
	}
}

func TestWriteShortAndList(t *testing.T) {
	cleanup := setupTestCmd(t)
	defer cleanup()

	output := captureOutput(t, func() {
		rootCmd.SetArgs([]string{"write", "short-lived note", "--type", "short"})
		rootCmd.Execute()
	})
	if !strings.Contains(output, "Created short memory:") {
		t.Fatalf("unexpected write output: %s", output)
	}

	output = captureOutput(t, func() {
		rootCmd.SetArgs([]string{"list", "--type", "short"})
		rootCmd.Execute()
	})
	if strings.Contains(output, "No memories found") {
		t.Fatalf("expected at least one short memory:\n%s", output)
	}
}
