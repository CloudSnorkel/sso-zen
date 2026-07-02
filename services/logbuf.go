// Package services owns cross-cutting glue. LogBuffer is the in-memory
// log ring used by the debug Log window so Windows users without a
// console (autostart launches the app detached) still have somewhere to
// read the running log.
package services

import (
	"strings"
	"sync"
)

// LogBuffer is a thread-safe ring of log lines. It satisfies io.Writer
// so a slog handler can write into it; the host UI reads via Lines().
//
// One Write call is expected to be one slog record (textHandler emits
// one record at a time). Records that contain embedded newlines are
// stored as a single line — fine for debug viewing.
type LogBuffer struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	revision uint64 // bumped on every mutation; UI consults to skip no-op refreshes
}

// NewLogBuffer creates a buffer that retains up to maxLines lines.
// maxLines <= 0 disables capture (writes are accepted and discarded).
func NewLogBuffer(maxLines int) *LogBuffer {
	return &LogBuffer{maxLines: maxLines}
}

// Write appends one log record, trimming the trailing newline. Always
// returns len(p), nil so it composes cleanly with io.MultiWriter.
func (b *LogBuffer) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	b.mu.Lock()
	if b.maxLines > 0 {
		b.lines = append(b.lines, line)
		if len(b.lines) > b.maxLines {
			b.lines = b.lines[len(b.lines)-b.maxLines:]
		}
		b.revision++
	}
	b.mu.Unlock()
	return len(p), nil
}

// Revision returns a counter that increments on every mutating call
// (Write, Resize when it changes content, Clear). UI consumers use it
// to short-circuit refresh work when nothing has changed since the
// last poll.
func (b *LogBuffer) Revision() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revision
}

// Lines returns a copy of the current buffer. Safe to call from any
// goroutine.
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Resize changes the retention limit. Existing lines beyond the new
// limit are dropped from the head; a non-positive limit disables
// capture and clears the buffer.
func (b *LogBuffer) Resize(maxLines int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	prevLen := len(b.lines)
	b.maxLines = maxLines
	if maxLines <= 0 {
		b.lines = nil
	} else if len(b.lines) > maxLines {
		b.lines = b.lines[len(b.lines)-maxLines:]
	}
	if len(b.lines) != prevLen {
		b.revision++
	}
}

// Clear empties the buffer without changing the retention limit.
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	if len(b.lines) > 0 {
		b.lines = nil
		b.revision++
	}
	b.mu.Unlock()
}
