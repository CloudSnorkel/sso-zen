//go:build linux

package services

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// StartupService manages Linux autostart functionality using .desktop files
type StartupService struct {
}

// NewStartupService creates a new startup service
func NewStartupService() *StartupService {
	return &StartupService{}
}

// getAutostartPath returns the path to the autostart .desktop file
func (ss *StartupService) getAutostartPath() (string, error) {
	// Try XDG_CONFIG_HOME first, then fall back to ~/.config
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		configHome = filepath.Join(homeDir, ".config")
	}

	autostartDir := filepath.Join(configHome, "autostart")
	if err := os.MkdirAll(autostartDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create autostart directory: %w", err)
	}

	desktopFile := "sso-zen.desktop"
	return filepath.Join(autostartDir, desktopFile), nil
}

// IsEnabled returns whether autostart is enabled
func (ss *StartupService) IsEnabled() (bool, error) {
	desktopPath, err := ss.getAutostartPath()
	if err != nil {
		return false, err
	}

	// Check if the .desktop file exists
	if _, err := os.Stat(desktopPath); os.IsNotExist(err) {
		return false, nil
	}

	// Read the file to check if it's properly configured
	data, err := os.ReadFile(desktopPath)
	if err != nil {
		return false, fmt.Errorf("failed to read desktop file: %w", err)
	}

	// Check if it contains Hidden=true or X-GNOME-Autostart-enabled=false
	content := string(data)
	if strings.Contains(content, "Hidden=true") {
		return false, nil
	}
	if strings.Contains(content, "X-GNOME-Autostart-enabled=false") {
		return false, nil
	}

	return true, nil
}

// Enable enables autostart by creating a .desktop file
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

	desktopPath, err := ss.getAutostartPath()
	if err != nil {
		return err
	}

	// Create the .desktop file content
	desktopContent := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=SSO Zen
Comment=Monitor local SSO sessions expiration
Exec=%s --background
Terminal=false
NoDisplay=false
X-GNOME-Autostart-enabled=true
`, exePath)

	// Write the .desktop file
	if err := os.WriteFile(desktopPath, []byte(desktopContent), 0644); err != nil {
		return fmt.Errorf("failed to write desktop file: %w", err)
	}

	slog.Info("Autostart enabled")
	return nil
}

// Disable disables autostart by removing the .desktop file
func (ss *StartupService) Disable() error {
	desktopPath, err := ss.getAutostartPath()
	if err != nil {
		return err
	}

	// Remove the .desktop file
	if err := os.Remove(desktopPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove desktop file: %w", err)
		}
		// Already removed
	}

	slog.Info("Autostart disabled")
	return nil
}
