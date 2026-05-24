package daemon

import (
	"testing"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/store"
)

func tempStore(t *testing.T) store.Store {
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

func TestExpireTaskNoOp(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("should not expire", store.PhaseInbox, store.CategoryInbox, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	task := &ExpireTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 (no-op), got %d", count)
	}
}

func TestDecayTaskNoOp(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("unused old", store.PhaseOrganized, store.CategoryKnowledge, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	task := &DecayTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 (no-op), got %d", count)
	}
}

func TestConsolidateTaskNoOp(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("duplicate content here", store.PhaseOrganized, store.CategoryKnowledge, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write1: %v", err)
	}
	_, err = s.Write("duplicate content here", store.PhaseOrganized, store.CategoryKnowledge, "global", nil, "manual")
	if err != nil {
		t.Fatalf("write2: %v", err)
	}

	task := &ConsolidateTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 (no-op), got %d", count)
	}

	memories, err := s.List(store.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("expected 2 memories (no deletion), got %d", len(memories))
	}
}

func TestRunOnce(t *testing.T) {
	s := tempStore(t)

	_, err := s.Write("short-lived", store.PhaseInbox, store.CategoryInbox, "global", nil, "manual")
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
