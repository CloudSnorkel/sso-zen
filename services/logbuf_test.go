package services

import (
	"reflect"
	"testing"
)

func TestLogBuffer_RingTrim(t *testing.T) {
	buf := NewLogBuffer(3)
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		if _, err := buf.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	got := buf.Lines()
	want := []string{"c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lines: got %v, want %v", got, want)
	}
}

func TestLogBuffer_ResizeShrink(t *testing.T) {
	buf := NewLogBuffer(5)
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		buf.Write([]byte(line + "\n"))
	}
	buf.Resize(2)
	got := buf.Lines()
	want := []string{"d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after shrink: got %v, want %v", got, want)
	}
}

func TestLogBuffer_ResizeToZeroDisables(t *testing.T) {
	buf := NewLogBuffer(3)
	buf.Write([]byte("a\n"))
	buf.Resize(0)
	if got := buf.Lines(); len(got) != 0 {
		t.Errorf("after resize(0): got %v, want empty", got)
	}
	buf.Write([]byte("b\n"))
	if got := buf.Lines(); len(got) != 0 {
		t.Errorf("after disabled write: got %v, want empty (capture disabled)", got)
	}
}

func TestLogBuffer_Clear(t *testing.T) {
	buf := NewLogBuffer(5)
	buf.Write([]byte("a\n"))
	buf.Write([]byte("b\n"))
	buf.Clear()
	if got := buf.Lines(); len(got) != 0 {
		t.Errorf("after Clear: got %v, want empty", got)
	}
	// Capture still works post-Clear.
	buf.Write([]byte("c\n"))
	if got := buf.Lines(); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("after post-Clear write: got %v, want [c]", got)
	}
}

func TestLogBuffer_StripsTrailingNewlineOnly(t *testing.T) {
	buf := NewLogBuffer(5)
	buf.Write([]byte("multi\nline\n"))
	// Only the trailing newline is trimmed; embedded ones stay.
	got := buf.Lines()
	want := []string{"multi\nline"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
