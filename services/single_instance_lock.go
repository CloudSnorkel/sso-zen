//go:build windows

package services

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/windows"
)

// SingleInstanceLock ensures only one instance of the application can run
type SingleInstanceLock struct {
	mutex windows.Handle
}

// NewSingleInstanceLock creates a new single instance lock
func NewSingleInstanceLock() *SingleInstanceLock {
	return &SingleInstanceLock{}
}

// Lock attempts to acquire the lock. Returns true if successful, false if another instance is running
func (sil *SingleInstanceLock) Lock() (bool, error) {
	// Create a named mutex (Windows-specific)
	// Global prefix ensures system-wide lock across all user sessions
	mutexName := "Global\\SSOZenSingleInstance"
	mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(mutexName))
	if err != nil {
		// Check if the error is because the mutex already exists
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create mutex: %w", err)
	}

	sil.mutex = mutex
	slog.Debug("Single instance lock acquired")
	return true, nil
}

// Unlock releases the lock
func (sil *SingleInstanceLock) Unlock() error {
	if sil.mutex != 0 {
		windows.CloseHandle(sil.mutex)
		sil.mutex = 0
		slog.Debug("Single instance lock released")
	}
	return nil
}

// TryLockAndExit attempts to acquire the lock and exits if another instance is running
func (sil *SingleInstanceLock) TryLockAndExit() {
	acquired, err := sil.Lock()
	if err != nil {
		slog.Error("Failed to check for existing instance", "error", err)
		os.Exit(1)
	}
	if !acquired {
		slog.Info("Another instance of SSO Zen is already running")
		os.Exit(0)
	}
}
