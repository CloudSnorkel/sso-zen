//go:build !windows

package services

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SingleInstanceLock ensures only one instance of the application can run
type SingleInstanceLock struct {
	file *os.File
}

// NewSingleInstanceLock creates a new single instance lock
func NewSingleInstanceLock() *SingleInstanceLock {
	return &SingleInstanceLock{}
}

// getLockFilePath returns the path to the lock file
func (sil *SingleInstanceLock) getLockFilePath() (string, error) {
	// Use a temp directory for the lock file
	tmpDir := os.TempDir()
	lockFile := filepath.Join(tmpDir, "sso-zen.lock")
	return lockFile, nil
}

// Lock attempts to acquire the lock. Returns true if successful, false if another instance is running
func (sil *SingleInstanceLock) Lock() (bool, error) {
	lockPath, err := sil.getLockFilePath()
	if err != nil {
		return false, fmt.Errorf("failed to get lock file path: %w", err)
	}

	// Try to create and lock the file
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Try to acquire an exclusive lock (non-blocking)
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		file.Close()
		// Check if it's because the file is already locked
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("failed to acquire lock: %w", err)
	}

	// Write the current process PID to the file for debugging
	pid := []byte(fmt.Sprintf("%d\n", os.Getpid()))
	if _, err := file.WriteAt(pid, 0); err != nil {
		// Not critical, but log it
		slog.Debug("Failed to write PID to lock file", "error", err)
	}

	sil.file = file
	slog.Debug("Single instance lock acquired", "lockFile", lockPath)
	return true, nil
}

// Unlock releases the lock
func (sil *SingleInstanceLock) Unlock() error {
	if sil.file != nil {
		unix.Flock(int(sil.file.Fd()), unix.LOCK_UN)
		sil.file.Close()
		// Optionally remove the lock file (though it's okay to leave it)
		lockPath, err := sil.getLockFilePath()
		if err == nil {
			os.Remove(lockPath)
		}
		sil.file = nil
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
