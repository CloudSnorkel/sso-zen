//go:build windows

package tray

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	procIsIconic   = user32.NewProc("IsIconic")
	procShowWindow = user32.NewProc("ShowWindow")
)

// restore un-minimizes w. GLFW's show only maps the window, so a window
// minimized to the taskbar stays minimized and Fyne has no API to undo that.
// Must run on the Fyne main goroutine.
func restore(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		wc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || wc.HWND == 0 {
			return
		}
		if iconic, _, _ := procIsIconic.Call(wc.HWND); iconic != 0 {
			procShowWindow.Call(wc.HWND, windows.SW_RESTORE)
		}
	})
}
