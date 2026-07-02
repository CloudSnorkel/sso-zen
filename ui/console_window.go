package ui

import (
	"bufio"
	"context"
	"fmt"
	"image/color"
	"io"
	"os/exec"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// consoleWindow represents a generic console window for running commands
type consoleWindow struct {
	window     fyne.Window
	output     *widget.RichText
	cmd        *exec.Cmd
	ctx        context.Context
	done       chan error
	closed     bool
	closeMutex sync.Mutex
}

// newConsoleWindow creates a new generic console window and sets up the UI
func newConsoleWindow(title string, ctx context.Context) *consoleWindow {
	console := &consoleWindow{
		ctx:        ctx,
		done:       make(chan error, 1),
		closed:     false,
		closeMutex: sync.Mutex{},
	}

	// Set up the UI in the Fyne thread and wait for completion
	var wg sync.WaitGroup
	wg.Add(1)

	fyne.Do(func() {
		defer wg.Done()

		// Use the current app instead of creating a new one
		currentApp := fyne.CurrentApp()
		if currentApp == nil {
			// Fallback to creating a new app if none exists
			currentApp = app.New()
		}

		window := currentApp.NewWindow(title)
		window.Resize(fyne.NewSize(600, 400))
		window.CenterOnScreen()

		output := widget.NewRichText()
		output.Wrapping = fyne.TextWrapWord
		output.Scroll = container.ScrollBoth

		console.window = window
		console.output = output

		console.show()
	})

	// Wait for UI setup to complete
	wg.Wait()

	return console
}

// show displays the console window
func (cw *consoleWindow) show() {
	// Create a black background rectangle
	blackBg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 255})

	// Create the main content with just the output (no cancel button)
	content := container.NewStack(blackBg, cw.output)

	cw.window.SetContent(content)

	cw.window.SetOnClosed(func() {
		if cw.cmd != nil {
			cw.cmd.Process.Kill()
			cw.cmd = nil
		}
	})

	cw.window.Show()
}

// startCommandWithEnv starts a command with an optional env override
// (nil = inherit the parent's environment).
func (cw *consoleWindow) startCommandWithEnv(command string, args, env []string) error {
	cmd := exec.CommandContext(cw.ctx, command, args...)
	cw.cmd = cmd
	if env != nil {
		cmd.Env = env
	}

	// On Windows, hide the terminal window that would otherwise appear
	setupWindowsSysProcAttr(cmd)

	// Set up output pipes
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Stream output in goroutines
	go cw.streamOutput(stdout, false) // stdout
	go cw.streamOutput(stderr, true)  // stderr

	// Wait for command completion in a goroutine
	go func() {
		err := cmd.Wait()
		cw.done <- err
	}()

	return nil
}

// streamOutput streams command output to the console
func (cw *consoleWindow) streamOutput(reader io.Reader, isError bool) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		// Create styled text segment with monospace font
		segment := &widget.TextSegment{
			Text: line + "\n",
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true},
			},
		}

		// Set colors based on output type
		if isError {
			// Red text for stderr
			segment.Style.ColorName = theme.ColorNameError
		} else {
			// Green text for stdout (terminal-like)
			segment.Style.ColorName = theme.ColorNameSuccess
		}

		// Add to output (thread-safe)
		fyne.Do(func() {
			cw.output.Segments = append(cw.output.Segments, segment)
			cw.output.Refresh()
		})
	}
}

// waitForCompletion waits for the command to complete
func (cw *consoleWindow) waitForCompletion() error {
	return <-cw.done
}

// RunCommand creates a console window, runs a command, and waits for completion
// Returns an error if the command fails, user cancels, or execution fails
// If the command fails, the console window remains open so users can see the error
func RunCommand(ctx context.Context, title, command string, args ...string) error {
	return RunUntilSuccess(ctx, title, []Command{{Name: command, Args: args}})
}

// Command describes one invocation to run inside a console window. Env
// is optional; nil inherits the parent process environment. Banner is
// printed in the console output before the command runs so the user
// can tell where one attempt ends and the next begins.
type Command struct {
	Name   string
	Args   []string
	Env    []string
	Banner string
}

// Option is one branch of a RunOptions call: a sequence of commands
// that must all complete with a zero exit for the option to "win".
type Option []Command

// RunUntilSuccess opens one console window and runs the given commands
// in order. The first command to exit zero wins — the window auto-closes
// and the function returns nil. If every command fails, the window
// stays open showing the accumulated output and the last error is
// returned.
func RunUntilSuccess(ctx context.Context, title string, cmds []Command) error {
	options := make([]Option, len(cmds))
	for i, c := range cmds {
		options[i] = Option{c}
	}
	return RunOptions(ctx, title, options)
}

// RunOptions opens one console window and tries each option in order.
// An option is a sequence of commands; an option wins when all of its
// commands complete with a zero exit. The first winning option closes
// the window and returns nil. If every option fails, the window stays
// open with the accumulated output and the last error is returned.
func RunOptions(ctx context.Context, title string, options []Option) error {
	if len(options) == 0 {
		return nil
	}
	console := newConsoleWindow(title, ctx)
	var lastErr error
	for _, opt := range options {
		failed := false
		for _, c := range opt {
			if c.Banner != "" {
				console.appendBanner(c.Banner)
			}
			if err := console.startCommandWithEnv(c.Name, c.Args, c.Env); err != nil {
				lastErr = fmt.Errorf("failed to start %s: %w", c.Name, err)
				failed = true
				break
			}
			if err := console.waitForCompletion(); err != nil {
				lastErr = err
				failed = true
				break
			}
		}
		if !failed {
			fyne.Do(func() { console.window.Close() })
			return nil
		}
	}
	return lastErr
}

// appendBanner writes a single styled line into the console output, used
// as a visual separator between sequential attempts.
func (cw *consoleWindow) appendBanner(text string) {
	segment := &widget.TextSegment{
		Text: text + "\n",
		Style: widget.RichTextStyle{
			TextStyle: fyne.TextStyle{Monospace: true, Bold: true},
		},
	}
	fyne.Do(func() {
		cw.output.Segments = append(cw.output.Segments, segment)
		cw.output.Refresh()
	})
}
