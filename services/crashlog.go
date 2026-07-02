package services

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// CrashLogPath returns the absolute path used for crash log entries.
func CrashLogPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "sso-zen")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "crash.log")
}

// LogPanic records a recovered panic value and stack trace to both the logger
// and the on-disk crash log. Safe to call from a deferred function.
func LogPanic(where string, recovered any) {
	stack := debug.Stack()
	slog.Error("panic recovered",
		"where", where,
		"value", fmt.Sprint(recovered),
		"stack", string(stack))
	f, err := os.OpenFile(CrashLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] panic in %s: %v\n%s\n",
		time.Now().Format(time.RFC3339), where, recovered, stack)
}

// SafeGo runs fn in a new goroutine that recovers from any panic and records
// it via LogPanic. Use for long-lived background goroutines where a panic
// should not bring down the whole process.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				LogPanic(name, r)
			}
		}()
		fn()
	}()
}
