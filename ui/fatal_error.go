package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// ShowFatalError opens a window with the given message, runs the Fyne
// event loop until the user dismisses it, then returns. The caller is
// expected to call os.Exit afterwards — this is only the user-visible
// half of a fatal startup error path, so the user sees the actual
// reason instead of a silent exit with detail only in the log.
func ShowFatalError(app fyne.App, message string) {
	win := app.NewWindow("SSO Zen — Startup error")
	win.Resize(fyne.NewSize(520, 260))
	win.CenterOnScreen()

	body := widget.NewLabel(message)
	body.Wrapping = fyne.TextWrapWord

	quit := widget.NewButton("Quit", func() {
		app.Quit()
	})
	quit.Importance = widget.HighImportance

	win.SetContent(container.NewBorder(
		nil,
		container.NewHBox(layout.NewSpacer(), quit),
		nil,
		nil,
		container.NewVScroll(body),
	))
	win.SetCloseIntercept(func() { app.Quit() })
	win.ShowAndRun()
}
