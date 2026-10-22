//go:build !windows

package tray

import "fyne.io/fyne/v2"

// restore is a no-op outside Windows: RequestFocus activates the window via
// the window manager, which also un-minimizes it.
func restore(fyne.Window) {}
