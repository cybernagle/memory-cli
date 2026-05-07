package daemon

import (
	"testing"
	"time"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Root:         t.TempDir(),
			ShortTermTTL: "1ms",
		},
	}
	s := store.New(cfg)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s
}

func TestExpireTask(t *testing.T) {
	s := tempStore(t)

	mem, err := s.Write("should expire", store.ShortTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	time.Sleep(2 * time.Millisecond)

	task := &ExpireTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 expired, got %d", count)
	}
	if _, err := s.Read(mem.ID); err == nil {
		t.Fatal("expired memory should be deleted")
	}
}

func TestExpireTaskDoesNotRemoveLongTerm(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("permanent", store.LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	task := &ExpireTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 expired, got %d", count)
	}
}

func TestDecayTask(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("unused old", store.LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	task := &DecayTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count < 0 {
		t.Fatalf("expected non-negative count, got %d", count)
	}
}

func TestConsolidateTaskDedup(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("duplicate content here", store.LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write1: %v", err)
	}
	_, err = s.Write("duplicate content here", store.LongTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write2: %v", err)
	}

	task := &ConsolidateTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 deduped, got %d", count)
	}

	memories, err := s.List(store.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory after consolidate, got %d", len(memories))
	}

	if len(memories) != 1 {
		t.Fatalf("expected 1 memory after consolidate, got %d", len(memories))
	}
	if memories[0].Content != "duplicate content here" {
		t.Fatalf("expected content preserved, got %q", memories[0].Content)
	}
}

func TestRunOnce(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("short-lived", store.ShortTerm, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	results := RunOnce(s)
	for name, count := range results {
		if count < 0 {
			t.Errorf("task %s returned error", name)
		}
	}
}
