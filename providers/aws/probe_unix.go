//go:build !windows

package aws

import "os/exec"

func hideConsoleWindow(_ *exec.Cmd) {}
