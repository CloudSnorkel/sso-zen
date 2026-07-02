//go:build !windows

package ui

import "os/exec"

// setupWindowsSysProcAttr is a no-op on non-Windows platforms
func setupWindowsSysProcAttr(cmd *exec.Cmd) {
	// No-op on Unix-like systems
}
