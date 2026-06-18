package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
	"github.com/cybernagle/memory-cli/internal/entity"
	"github.com/cybernagle/memory-cli/internal/factprocessor"
	"github.com/cybernagle/memory-cli/internal/health"
	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/notify"
	"github.com/cybernagle/memory-cli/internal/plugin"
	"github.com/cybernagle/memory-cli/internal/store"
	"github.com/cybernagle/memory-cli/internal/transport"

	"github.com/cybernagle/memory-cli/internal/api"
)

var serveInterval string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the memory processing daemon with Unix socket transport",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}
		interval, err := time.ParseDuration(serveInterval)
		if err != nil {
			return fmt.Errorf("invalid interval: %w", err)
		}

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		decayThreshold, err := parseDurationConfig(cfg.Daemon.DecayThreshold)
		if err != nil {
			return fmt.Errorf("invalid decay_threshold: %w", err)
		}
		lockPath := cfg.Storage.Root + "/.daemon.lock"
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			if os.IsExist(err) {
				// Check if the PID is still running; if not, remove stale lock
				if data, readErr := os.ReadFile(lockPath); readErr == nil {
					var pid int
					fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
					if pid > 0 {
						if proc, _ := os.FindProcess(pid); proc.Signal(syscall.Signal(0)) != nil {
							os.Remove(lockPath)
							lockFile, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
						}
					}
				}
			}
			if err != nil {
				return fmt.Errorf("daemon already running (lock file: %s); remove %s if stale", lockPath, lockPath)
			}
		}
		defer func() {
			lockFile.Close()
			os.Remove(lockPath)
		}()
		lockFile.WriteString(fmt.Sprintf("%d\n", os.Getpid()))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		// Build notifier
		var notifier *notify.MultiNotifier
		if cfg.Notification.Enabled {
			notifier = notify.NewMultiNotifier(notify.Config{
				DingDingWebhook: cfg.Notification.DingDingWebhook,
				DingDingSecret:  cfg.Notification.DingDingSecret,
				WeChatWebhook:   cfg.Notification.WeChatWebhook,
			})
		}

		// Start daemon
		d := daemon.New(s, interval, decayThreshold, cfg.Daemon.UpgradeAccess, notifier)
		var pipelineReg *plugin.Registry

		// Reminder task — always enabled (doesn't need LLM, just checks the reminders table
		// every tick and fires macOS notifications for due items).
		reminderTask := daemon.NewReminderTask(cfg)
		if sqliteStore, ok := s.(*store.SqliteStore); ok {
			reminderTask.SetStore(sqliteStore)
		}
		d.AddTask(reminderTask)

		// Wire LLM processor if an API key is available (MEMORY_LLM_API_KEY / legacy ANTHROPIC_*).
		// A single GLM-4.5-Flash client serves extraction, consolidation, and enrichment.
		if llmClient, err := llm.NewClient(llm.Config{}); err == nil {
			if sqliteStore, ok := s.(*store.SqliteStore); ok {
				fmt.Printf("LLM ready: model=%s base=%s\n", llmClient.Model(), llmClient.BaseURL())
				d.AddTask(&daemon.ConsolidateLLMTask{Store: sqliteStore, LLM: llmClient})
				d.AddTask(&daemon.EnrichTagsTask{Store: sqliteStore, LLM: llmClient})

				// Use plugin pipeline if enabled, otherwise legacy processor
				if cfg.Pipeline.Enabled {
					pipelineReg = plugin.NewRegistry()
					entityComp := entity.NewEntityComponent()
					if err := entityComp.Init(context.Background(), sqliteStore.DB()); err != nil {
						fmt.Fprintf(os.Stderr, "entity init error: %v\n", err)
					} else {
						pipelineReg.RegisterComponent(entityComp)
						fp := factprocessor.New(llmClient, entityComp)
						pipelineReg.RegisterProcessor(fp)

						// Register ingest adapters
						adapters := getAdapters("")
						for _, a := range adapters {
							pipelineReg.RegisterIngest(plugin.NewIngestAdapter(a))
						}

						router := func(ctx context.Context, item plugin.DataItem) error {
							if item.Confidence < 0.5 {
								log.Printf("[pipeline] discarding low confidence (%.2f): %.80s", item.Confidence, item.Data["content"])
								return nil
							}
							mem := factprocessor.NewMemoryFromDataItem(item)
							return s.IngestMemory(mem)
						}
						engine := plugin.NewPipelineEngine(pipelineReg, s, router)
						threshold := cfg.Pipeline.Threshold
						if threshold == 0 {
							threshold = 100
						}
						d.AddTask(&daemon.PipelineTask{Engine: engine, Threshold: threshold})
						fmt.Printf("Plugin pipeline enabled (threshold: %d)\n", threshold)
					}
				} else {
					d.WithProcessor(daemon.ProcessConfig{
						SqliteStore: sqliteStore,
						LLMClient:   llmClient,
						Threshold:   100,
					})
					fmt.Println("LLM processor enabled (threshold: 100)")
				}
			}
		} else {
			fmt.Printf("LLM processor disabled: %v\n", err)
		}

		go func() {
			if err := d.Run(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "daemon error: %v\n", err)
			}
		}()

		// Start Unix socket server
		socketPath := cfg.Storage.Root + "/memory.sock"
		srv := transport.NewSocketServer(socketPath, s)
		go func() {
			if err := srv.Listen(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "socket error: %v\n", err)
			}
		}()

		// Start heartbeat
		heartbeatPath := cfg.Storage.Root + "/.heartbeat"
		checker := health.NewChecker(heartbeatPath)
		go checker.HeartbeatLoop(ctx)

		fmt.Printf("Memory daemon running (socket: %s, interval: %s)\n", socketPath, interval)

		// Start HTTP API if enabled
		if cfg.API.Enabled && cfg.API.Listen != "" {
			httpSrv := transport.NewHTTPServer(cfg.API.Keys, s)
			go func() {
				fmt.Printf("Memory API listening on %s\n", cfg.API.Listen)
				server := &http.Server{
					Addr:         cfg.API.Listen,
					Handler:      httpSrv.Handler(),
					ReadTimeout:  15 * time.Second,
					WriteTimeout: 30 * time.Second,
					IdleTimeout:  60 * time.Second,
				}
				if err := server.ListenAndServe(); err != nil {
					fmt.Fprintf(os.Stderr, "API server error: %v\n", err)
				}
			}()
		}

		// Start REST API server for agent integration
		dbPath := cfg.Storage.SQLitePath
		if dbPath == "" {
			dbPath = config.SQLiteDefaultPath()
		}
		apiSrv := api.NewServer(s, dbPath, cfg.API.Keys)
		apiSrv.SetRegistry(pipelineReg)
		apiAddr := "127.0.0.1:8765"
		go func() {
			fmt.Printf("Memory REST API listening on %s\n", apiAddr)
			apiHTTP := &http.Server{
				Addr:         apiAddr,
				Handler:      apiSrv,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			if err := apiHTTP.ListenAndServe(); err != nil {
				fmt.Fprintf(os.Stderr, "REST API error: %v\n", err)
			}
		}()

		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
		return nil
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveInterval, "interval", "60s", "Processing interval")
	rootCmd.AddCommand(serveCmd)
}

func parseDurationConfig(s string) (time.Duration, error) {
	if s == "" {
		return 30 * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid day duration: %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
