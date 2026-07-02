//go:build windows

package ui

import (
	"os/exec"
	"syscall"
)

// setupWindowsSysProcAttr configures Windows-specific SysProcAttr to hide the terminal window
func setupWindowsSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
}
