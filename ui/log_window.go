package ui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"sso-zen/services"

	"fyne.io/fyne/v2"
)

// LogWindow exports the buffer contents to a temp file and opens it
// in the system default viewer (Notepad / TextEdit / xdg-open's
// pick). An in-process Fyne window was tried first but Fyne's TextGrid
// got noticeably slow on multi-thousand-line buffers and didn't
// support text selection; the OS-native viewer side-steps both for
// free.
//
// The Fyne app handle is kept on the struct so the constructor
// signature stays stable across the LogBuffer-only refactor — callers
// don't need to be updated. Today it isn't otherwise used.
type LogWindow struct {
	app    fyne.App
	buffer *services.LogBuffer
}

// NewLogWindow constructs a LogWindow bound to buffer.
func NewLogWindow(app fyne.App, buffer *services.LogBuffer) *LogWindow {
	return &LogWindow{app: app, buffer: buffer}
}

// Show writes the current buffer to a temp file and hands it off to
// the OS's default viewer. The temp file is cleaned up in a detached
// goroutine after the launcher process exits (xdg-open / open / cmd
// /c start all return quickly even though the real editor stays
// running) plus a 5-second grace window so the editor has time to
// actually read it. Errors are logged but not surfaced — Show is
// fire-and-forget from menu callbacks.
func (lw *LogWindow) Show() {
	if err := lw.dumpAndOpen(); err != nil {
		slog.Error("Failed to open log file", "error", err)
	}
}

func (lw *LogWindow) dumpAndOpen() error {
	tmp, err := os.CreateTemp("", "sso-zen-*.log")
	if err != nil {
		return fmt.Errorf("create temp log: %w", err)
	}
	path := tmp.Name()

	body := strings.Join(lw.buffer.Lines(), "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(path)
		return fmt.Errorf("write temp log %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("close temp log %s: %w", path, err)
	}

	cmd, err := openWithSystemViewer(path)
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("launch system viewer for %s: %w", path, err)
	}
	slog.Info("Opened log file in system viewer", "path", path)

	go cleanupAfter(cmd, path)
	return nil
}

// openWithSystemViewer fires the OS's "open with default app" hook.
// All three launchers return after dispatch — the real viewer keeps
// running independently — so cmd.Wait below completes quickly. The
// 5-second grace in cleanupAfter is for whatever the launcher passed
// the file to (the actual editor) to grab a handle on the contents.
func openWithSystemViewer(path string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		// `start ""` — the first quoted arg is the window title
		// (empty), so the path becomes the file argument. Without
		// the empty title, start interprets a quoted path as the
		// title and fails to open it.
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	setupWindowsSysProcAttr(cmd)
	return cmd, cmd.Start()
}

// cleanupAfter waits for the launcher to exit, sleeps 5s so the real
// viewer has a chance to read the file, then removes it. Best effort:
// if Windows is still holding a delete-blocking handle (Notepad on
// older builds, mainly) we just log and move on; the OS will reclaim
// the temp eventually.
func cleanupAfter(cmd *exec.Cmd, path string) {
	if err := cmd.Wait(); err != nil {
		slog.Debug("System viewer launcher exited non-zero", "path", path, "error", err)
	}
	time.Sleep(5 * time.Second)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("Could not remove temp log", "path", path, "error", err)
	}
}
