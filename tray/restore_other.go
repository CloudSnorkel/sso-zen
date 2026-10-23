//go:build !windows

package tray

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// restore un-minimizes w on Wayland, which gives clients no way to
// un-minimize a window or even tell that it is minimized, and where Fyne's
// RequestFocus is a no-op. Hiding destroys the xdg toplevel and showing
// creates a fresh one, which the compositor maps un-minimized. On X11 and
// macOS RequestFocus already activates and un-minimizes the window.
// Must run on the Fyne main goroutine.
func restore(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	var wayland bool
	nw.RunNative(func(ctx any) {
		_, wayland = ctx.(driver.WaylandWindowContext)
	})
	if wayland {
		w.Hide()
		w.Show()
	}
}
