package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/daemon"
)

var serveInterval string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the memory processing daemon",
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
		lockPath := cfg.Storage.Root + "/.daemon.lock"
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("daemon already running (lock file: %s); remove %s if stale", lockPath, lockPath)
			}
			return fmt.Errorf("create lock file: %w", err)
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

		d := daemon.New(s, interval)
		go func() {
			<-sigCh
			fmt.Println("\nShutting down...")
			cancel()
		}()

		return d.Run(ctx)
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveInterval, "interval", "60s", "Processing interval")
	rootCmd.AddCommand(serveCmd)
}
