package daemon

import (
	"context"
	"log"
	"time"

	"github.com/cybernagle/memory-cli/internal/store"
)

type Daemon struct {
	store    *store.Store
	interval time.Duration
	tasks    []Task
}

type Task interface {
	Name() string
	Run(s *store.Store) (int, error)
}

func New(s *store.Store, interval time.Duration) *Daemon {
	return &Daemon{
		store:    s,
		interval: interval,
		tasks: []Task{
			&ExpireTask{},
			&DecayTask{},
			&UpgradeTask{},
			&ConsolidateTask{},
		},
	}
}

func (d *Daemon) Run(ctx context.Context) error {
	log.Printf("Memory daemon started (interval: %s)", d.interval)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.runTasks()

	for {
		select {
		case <-ctx.Done():
			log.Println("Memory daemon stopped")
			return nil
		case <-ticker.C:
			d.runTasks()
		}
	}
}

func (d *Daemon) runTasks() {
	for _, task := range d.tasks {
		count, err := task.Run(d.store)
		if err != nil {
			log.Printf("[%s] error: %v", task.Name(), err)
			continue
		}
		if count > 0 {
			log.Printf("[%s] processed %d items", task.Name(), count)
		}
	}
}

func RunOnce(s *store.Store) map[string]int {
	results := make(map[string]int)
	tasks := []Task{
		&ExpireTask{},
		&DecayTask{},
		&UpgradeTask{},
		&ConsolidateTask{},
	}
	for _, task := range tasks {
		count, err := task.Run(s)
		if err != nil {
			results[task.Name()] = -1
			continue
		}
		results[task.Name()] = count
	}
	return results
}
