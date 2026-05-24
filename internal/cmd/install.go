package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install memory daemon as a launchd service (macOS)",
	RunE: func(cmd *cobra.Command, args []string) error {
		home := config.MustHomeDir()
		label := "com.cybernagle.memory-cli"

		binPath, err := exec.LookPath("memory")
		if err != nil {
			binPath, err = os.Executable()
			if err != nil {
				return fmt.Errorf("cannot find memory binary: %w", err)
			}
		}

		logDir := filepath.Join(home, ".memory", "logs")
		os.MkdirAll(logDir, 0755)

		configPath := filepath.Join(home, ".memory", "config.yaml")

		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
        <string>--config</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s/daemon.log</string>
    <key>StandardErrorPath</key>
    <string>%s/daemon.err</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin</string>
    </dict>
</dict>
</plist>`, label, binPath, configPath, logDir, logDir)

		plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		os.MkdirAll(filepath.Dir(plistPath), 0755)

		if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
			return fmt.Errorf("write plist: %w", err)
		}

		// Unload if already loaded
		exec.Command("launchctl", "unload", plistPath).Run()

		// Load
		if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
			return fmt.Errorf("load plist: %w", err)
		}

		fmt.Printf("Memory daemon installed and started.\n")
		fmt.Printf("  Plist: %s\n", plistPath)
		fmt.Printf("  Logs:  %s/daemon.log\n", logDir)
		fmt.Printf("\nTo stop:  launchctl unload %s\n", plistPath)
		fmt.Printf("To start: launchctl load %s\n", plistPath)
		fmt.Printf("To uninstall: memory uninstall\n")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall memory daemon launchd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		home := config.MustHomeDir()
		label := "com.cybernagle.memory-cli"
		plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")

		exec.Command("launchctl", "unload", plistPath).Run()
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove plist: %w", err)
		}

		fmt.Println("Memory daemon uninstalled.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
}
