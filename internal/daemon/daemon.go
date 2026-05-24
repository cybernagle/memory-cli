package daemon

import (
	"context"
	"log"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/notify"
	"github.com/cybernagle/memory-cli/internal/store"
)

type Daemon struct {
	store    store.Store
	interval time.Duration
	tasks    []Task
	notifier *notify.MultiNotifier
}

type Task interface {
	Name() string
	Run(s store.Store) (int, error)
}

type ProcessConfig struct {
	SqliteStore *store.SqliteStore
	LLMClient   *llm.Client
	Threshold   int
}

func New(s store.Store, interval time.Duration, decayThreshold time.Duration, upgradeAccess int, notifier *notify.MultiNotifier) *Daemon {
	if decayThreshold == 0 {
		decayThreshold = 30 * 24 * time.Hour
	}
	if upgradeAccess == 0 {
		upgradeAccess = 3
	}
	return &Daemon{
		store:    s,
		interval: interval,
		notifier: notifier,
		tasks: []Task{
			&ExpireTask{},
			&DecayTask{Threshold: decayThreshold},
			&UpgradeTask{Threshold: upgradeAccess},
			&ConsolidateTask{},
		},
	}
}

func (d *Daemon) AddTask(t Task) { d.tasks = append(d.tasks, t) }

// WithProcessor enables the LLM-powered extraction pipeline.
func (d *Daemon) WithProcessor(cfg ProcessConfig) *Daemon {
	if cfg.SqliteStore != nil && cfg.LLMClient != nil {
		d.tasks = append(d.tasks, &ProcessTask{
			Store:     cfg.SqliteStore,
			LLM:       cfg.LLMClient,
			Threshold: cfg.Threshold,
		})
	}
	return d
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
			if d.notifier != nil {
				msg := notify.FormatMessage("Memory Task Error",
					"**Task**: "+task.Name()+"\n**Error**: "+err.Error())
				d.notifier.Send("Memory Task Error", msg)
			}
			continue
		}
		if count > 0 {
			log.Printf("[%s] processed %d items", task.Name(), count)
		}
	}
}

func RunOnce(s store.Store) map[string]int {
	results := make(map[string]int)
	tasks := []Task{
		&ExpireTask{},
		&DecayTask{Threshold: 30 * 24 * time.Hour},
		&UpgradeTask{Threshold: 3},
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
