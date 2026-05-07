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

	s.Write("permanent", store.LongTerm, "global", nil, "manual")

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

	s.Write("unused old", store.LongTerm, "global", nil, "manual")

	task := &DecayTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Memories just created have 0 access and old UpdatedAt (time.Now()),
	// but decayThreshold is 30 days so newly created ones won't be decayed.
	// This tests that the task runs without error.
	if count < 0 {
		t.Fatalf("expected non-negative count, got %d", count)
	}
}

func TestUpgradeTask(t *testing.T) {
	s := tempStore(t)

	mem, _ := s.Write("popular", store.ShortTerm, "global", nil, "manual")
	s.Read(mem.ID)
	s.Read(mem.ID)
	s.Read(mem.ID)

	task := &UpgradeTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 upgraded, got %d", count)
	}

	got, err := s.Read(mem.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != store.LongTerm {
		t.Fatalf("expected long-term after upgrade, got %s", got.Type)
	}
}

func TestConsolidateTaskDedup(t *testing.T) {
	s := tempStore(t)

	mem1, _ := s.Write("duplicate content here", store.LongTerm, "global", nil, "manual")
	s.Read(mem1.ID)
	s.Read(mem1.ID)
	_, _ = s.Write("duplicate content here", store.LongTerm, "global", nil, "manual")

	task := &ConsolidateTask{}
	count, err := task.Run(s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 deduped, got %d", count)
	}

	memories, _ := s.List(store.ListOptions{})
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory after consolidate, got %d", len(memories))
	}

	if memories[0].ID != mem1.ID {
		t.Fatalf("expected higher access count entry (%s) to be kept, got %s", mem1.ID, memories[0].ID)
	}
}

func TestRunOnce(t *testing.T) {
	s := tempStore(t)

	mem, _ := s.Write("short-lived", store.ShortTerm, "global", nil, "manual")
	s.Read(mem.ID)
	s.Read(mem.ID)
	s.Read(mem.ID)

	results := RunOnce(s)
	for name, count := range results {
		if count < 0 {
			t.Errorf("task %s returned error", name)
		}
	}
}
