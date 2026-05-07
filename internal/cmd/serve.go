package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
