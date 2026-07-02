package aws

import "context"

// Command describes one invocation to run inside a CommandRunner's
// console. Env is optional; nil inherits the parent process environment.
// Banner is printed before the command runs so the user can tell where
// one attempt ends and the next begins.
type Command struct {
	Name   string
	Args   []string
	Env    []string
	Banner string
}

// Option is one branch of a RunOptions call: a sequence of commands that
// must all complete with a zero exit for the option to "win".
type Option []Command

// CommandRunner runs a sequence of Options inside whatever console the
// host application provides. The provider doesn't care whether that's a
// Fyne window, a plain terminal, or a test stub — only that the runner
// can execute the commands and report success/failure.
type CommandRunner interface {
	RunOptions(ctx context.Context, title string, options []Option) error
}
