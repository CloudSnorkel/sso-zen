//go:build windows

package services

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// StartupService manages Windows autostart functionality
type StartupService struct {
}

// NewStartupService creates a new startup service
func NewStartupService() *StartupService {
	return &StartupService{}
}

// IsEnabled returns whether autostart is enabled
func (ss *StartupService) IsEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("failed to open registry key: %w", err)
	}
	defer k.Close()

	// Check if our app is in the startup registry
	appName := "SSO Zen"
	_, _, err = k.GetStringValue(appName)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read registry value: %w", err)
	}

	return true, nil
}

// Enable enables autostart
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

	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open registry key: %w", err)
	}
	defer k.Close()

	appName := "SSO Zen"
	// Quote path in case of spaces and add background flag so window stays hidden on autostart
	cmd := fmt.Sprintf("\"%s\" --background", exePath)
	if err := k.SetStringValue(appName, cmd); err != nil {
		return fmt.Errorf("failed to set registry value: %w", err)
	}

	slog.Info("Autostart enabled")
	return nil
}

// Disable disables autostart
func (ss *StartupService) Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open registry key: %w", err)
	}
	defer k.Close()

	appName := "SSO Zen"
	if err := k.DeleteValue(appName); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			// Already disabled
			return nil
		}
		return fmt.Errorf("failed to delete registry value: %w", err)
	}

	slog.Info("Autostart disabled")
	return nil
}
