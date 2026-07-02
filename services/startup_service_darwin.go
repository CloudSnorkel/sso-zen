//go:build darwin

package services

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StartupService manages macOS autostart functionality using Launch Agents
type StartupService struct {
}

// NewStartupService creates a new startup service
func NewStartupService() *StartupService {
	return &StartupService{}
}

// getLaunchAgentPath returns the path to the Launch Agent plist file
func (ss *StartupService) getLaunchAgentPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	launchAgentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}

	// Use reverse domain notation for the plist filename
	plistFile := "com.cloudsnorkel.ssozen.plist"
	return filepath.Join(launchAgentsDir, plistFile), nil
}

// IsEnabled returns whether autostart is enabled
func (ss *StartupService) IsEnabled() (bool, error) {
	plistPath, err := ss.getLaunchAgentPath()
	if err != nil {
		return false, err
	}

	// Check if the plist file exists
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return false, nil
	}

	// Also check if it's loaded with launchctl
	cmd := exec.Command("launchctl", "list")
	output, err := cmd.Output()
	if err != nil {
		// If launchctl fails, just check if file exists
		return true, nil
	}

	// Check if our service ID is in the list
	serviceID := "com.cloudsnorkel.ssozen"
	return strings.Contains(string(output), serviceID), nil
}

// Enable enables autostart by creating a Launch Agent plist file
func (ss *StartupService) Enable() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Get absolute path
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	plistPath, err := ss.getLaunchAgentPath()
	if err != nil {
		return err
	}

	// Create the plist content
	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.cloudsnorkel.ssozen</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>`, exePath)

	// Write the plist file
	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("failed to write plist file: %w", err)
	}

	// Load the Launch Agent
	cmd := exec.Command("launchctl", "load", plistPath)
	if err := cmd.Run(); err != nil {
		// If load fails, try unload first (in case it's already loaded)
		exec.Command("launchctl", "unload", plistPath).Run()
		if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
			return fmt.Errorf("failed to load Launch Agent: %w", err)
		}
	}

	slog.Info("Autostart enabled")
	return nil
}

// Disable disables autostart by removing the Launch Agent
func (ss *StartupService) Disable() error {
	plistPath, err := ss.getLaunchAgentPath()
	if err != nil {
		return err
	}

	// Unload the Launch Agent if it's loaded
	cmd := exec.Command("launchctl", "unload", plistPath)
	if err := cmd.Run(); err != nil {
		// Ignore errors if it's not loaded
		slog.Debug("Launch Agent not loaded, continuing with removal")
	}

	// Remove the plist file
	if err := os.Remove(plistPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove plist file: %w", err)
		}
		// Already removed
	}

	slog.Info("Autostart disabled")
	return nil
}
