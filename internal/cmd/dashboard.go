package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/dashboard"
	"github.com/cybernagle/memory-cli/internal/llm"
)

var dashboardPort int

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start web dashboard for memory visualization",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		addr := fmt.Sprintf(":%d", dashboardPort)

		var llmClient *llm.Client
		if c, err := llm.NewClient(llm.Config{}); err == nil {
			llmClient = c
		}

		srv := dashboard.NewServer(s, llmClient)

		url := fmt.Sprintf("http://localhost:%d", dashboardPort)
		fmt.Printf("Memory dashboard running at %s\n", url)

		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(url)
		}()
		return srv.ListenAndServe(addr)
	},
}

func init() {
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 8080, "HTTP server port")
	dashboardCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if dashboardPort <= 0 || dashboardPort > 65535 {
			return fmt.Errorf("port must be between 1 and 65535, got %d", dashboardPort)
		}
		return nil
	}
	rootCmd.AddCommand(dashboardCmd)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	if cmd != nil {
		cmd.Start()
	}
}
