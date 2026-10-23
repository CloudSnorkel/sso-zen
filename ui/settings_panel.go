package ui

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"sso-zen/services"
	"sso-zen/settings"
	"sso-zen/sso"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ProviderSettingsUI is implemented by providers that want to render a
// settings card in the global settings panel. infoFactory builds the
// shared info-popup button — providers reuse it so the popup styling
// stays consistent across cards.
type ProviderSettingsUI interface {
	SettingsUI(infoFactory func(title, markdown string) *widget.Button) fyne.CanvasObject
}

// numericEntry wraps a short number-entry field in a fixed-width
// container so it doesn't collapse to the four-digit "100…" stub Fyne
// otherwise renders inside an HBox. Height is taken from the entry's
// own MinSize so the validator checkmark stays vertically centred.
func numericEntry(entry *widget.Entry) fyne.CanvasObject {
	h := entry.MinSize().Height
	if h < 36 {
		h = 36
	}
	return container.New(layout.NewGridWrapLayout(fyne.NewSize(110, h)), entry)
}

// infoButton returns a small info-icon button that opens a popup
// showing the given markdown body. Non-modal so a tap outside dismisses
// it, with a Close button for keyboard / touch users who prefer that.
// Keeps long explainer text out of the settings layout (which can render
// inline RichText awkwardly when the window is narrow).
func infoButton(title, markdown string) *widget.Button {
	btn := widget.NewButtonWithIcon("", theme.InfoIcon(), nil)
	btn.Importance = widget.LowImportance
	btn.OnTapped = func() {
		canvas := fyne.CurrentApp().Driver().CanvasForObject(btn)
		if canvas == nil {
			return
		}

		body := widget.NewRichTextFromMarkdown(markdown)
		body.Wrapping = fyne.TextWrapWord

		header := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

		var popup *widget.PopUp
		closeBtn := widget.NewButton("Close", func() { popup.Hide() })
		closeRow := container.NewHBox(layout.NewSpacer(), closeBtn)

		content := container.NewBorder(header, closeRow, nil, nil, container.NewVScroll(body))
		popup = widget.NewPopUp(container.NewPadded(content), canvas)

		showCenteredPopup(popup, canvas, 520, 360)
	}
	return btn
}

// showCenteredPopup resizes popup to min(canvas-margin, maxW/maxH) and
// shows it centered on the canvas. NewPopUp's default Show() lands the
// popup at the canvas origin, which looks broken — manual ShowAtPosition
// gives the same visual centering ModalPopUp does for free.
func showCenteredPopup(popup *widget.PopUp, canvas fyne.Canvas, maxW, maxH float32) {
	size := canvas.Size()
	popupSize := fyne.NewSize(min(size.Width-40, maxW), min(size.Height-80, maxH))
	popup.Resize(popupSize)
	popup.ShowAtPosition(fyne.NewPos(
		(size.Width-popupSize.Width)/2,
		(size.Height-popupSize.Height)/2,
	))
}

// CreateSettingsPanel creates a settings panel that can be embedded in
// the main window. Each registered provider that implements
// ProviderSettingsUI contributes its own card between the General and
// Notifications cards. logWindow is the debug log viewer the
// Diagnostics card opens.
func CreateSettingsPanel(settingsService *settings.Store, startupService *services.StartupService, providers []sso.Provider, logWindow *LogWindow, backToSessions func()) *fyne.Container {
	current := settingsService.Settings()

	// Header with back button
	backButton := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		backToSessions()
	})
	backButton.Importance = widget.LowImportance

	headerTitle := widget.NewLabel("Settings")
	headerTitle.TextStyle.Bold = true

	headerContainer := container.NewHBox(
		backButton,
		headerTitle,
	)

	// Start with computer toggle
	startWithComputerToggle := widget.NewCheck("Start with computer", func(checked bool) {
		var err error
		if checked {
			err = startupService.Enable()
		} else {
			err = startupService.Disable()
		}

		if err != nil {
			slog.Error("Failed to change autostart state", "error", err)
			// Just log the error - user will see checkbox didn't change
			return
		}

		// Update settings
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.StartWithComputer = checked
		})
		slog.Info("Updated setting: StartWithComputer", "value", checked)
	})

	// Set initial state
	if enabled, err := startupService.IsEnabled(); err == nil {
		startWithComputerToggle.SetChecked(enabled)
	}

	// Notify when expiring toggle
	notifyExpiringToggle := widget.NewCheck("Notify when session is expiring soon", func(checked bool) {
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.NotifyWhenExpiring = checked
		})
		slog.Info("Updated setting: NotifyWhenExpiring", "value", checked)
	})
	notifyExpiringToggle.SetChecked(current.NotifyWhenExpiring)

	notifyExpiredToggle := widget.NewCheck("Notify when session expires", func(checked bool) {
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.NotifyWhenExpired = checked
		})
		slog.Info("Updated setting: NotifyWhenExpired", "value", checked)
	})
	notifyExpiredToggle.SetChecked(current.NotifyWhenExpired)

	// Expiry notification threshold - custom spinner with up/down buttons
	thresholdMinutes := int(current.ExpiryNotificationThreshold.Minutes())

	// Create number entry with +/- buttons
	thresholdEntry := widget.NewEntry()
	thresholdEntry.SetText(strconv.Itoa(thresholdMinutes))
	thresholdEntry.Validator = func(s string) error {
		var val int
		if _, err := fmt.Sscanf(s, "%d", &val); err != nil {
			return fmt.Errorf("must be a number")
		}
		if val < 1 || val > 1440 {
			return fmt.Errorf("must be between 1 and 1440 minutes")
		}
		return nil
	}

	// Update threshold live as the user types — no need to press Enter.
	thresholdEntry.OnChanged = func(s string) {
		var val int
		if _, err := fmt.Sscanf(s, "%d", &val); err == nil && val >= 1 && val <= 1440 {
			settingsService.UpdateSettings(func(s *settings.Settings) {
				s.ExpiryNotificationThreshold = time.Duration(val) * time.Minute
			})
			slog.Info("Updated setting: ExpiryNotificationThreshold (minutes)", "value", val)
		}
	}

	thresholdLabel := widget.NewLabel("Notify minutes before expiry:")
	thresholdContainer := container.NewHBox(
		thresholdLabel,
		numericEntry(thresholdEntry),
		widget.NewLabel("minutes"),
	)

	probeNearExpiryToggle := widget.NewCheck("Probe AWS near expiry to confirm status", func(checked bool) {
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.ProbeNearExpiry = checked
		})
		slog.Info("Updated setting: ProbeNearExpiry", "value", checked)
	})
	probeNearExpiryToggle.SetChecked(current.ProbeNearExpiry)

	probeInfo := infoButton("About probing", `Checks whether your session is really expiring or whether the AWS SDK has silently refreshed your token. Avoids false "expiring soon" alerts.

**Trade-off:** the check writes fresh credentials to your AWS cache directories even while you're idle, which weakens protection against malware that reads those files. Off by default for that reason.`)

	// Build provider-scoped cards.
	cards := []fyne.CanvasObject{
		widget.NewCard("General", "", container.NewVBox(startWithComputerToggle)),
	}
	for _, p := range providers {
		if ui, ok := p.(ProviderSettingsUI); ok {
			cards = append(cards, ui.SettingsUI(infoButton))
		}
	}
	cards = append(cards, widget.NewCard("Notifications", "", container.NewVBox(
		notifyExpiringToggle,
		thresholdContainer,
		notifyExpiredToggle,
		container.NewHBox(probeNearExpiryToggle, probeInfo),
	)))
	cards = append(cards, diagnosticsCard(settingsService, logWindow, current))
	content := container.NewVBox(cards...)

	// Create bordered layout with header and scrollable content
	scrollContent := container.NewVScroll(content)

	return container.NewBorder(
		headerContainer, // Top
		nil,             // Bottom
		nil,             // Left
		nil,             // Right
		scrollContent,   // Center
	)
}

// diagnosticsCard builds the Diagnostics card: the log-buffer size
// control + an Open Log Window button. The buffer-size entry persists
// on valid input; main's settings drain calls LogBuffer.Resize.
func diagnosticsCard(settingsService *settings.Store, logWindow *LogWindow, current *settings.Settings) fyne.CanvasObject {
	bufferEntry := widget.NewEntry()
	bufferEntry.SetText(strconv.Itoa(current.LogBufferLines))
	bufferEntry.Validator = func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if v < 0 || v > 1_000_000 {
			return fmt.Errorf("must be between 0 and 1000000")
		}
		return nil
	}
	bufferEntry.OnChanged = func(s string) {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 || v > 1_000_000 {
			return
		}
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.LogBufferLines = v
		})
		slog.Info("Updated setting: LogBufferLines", "value", v)
	}

	openButton := widget.NewButtonWithIcon("Open Log", theme.DocumentIcon(), func() {
		if logWindow != nil {
			logWindow.Show()
		}
	})

	info := infoButton("About logs", `Open Log saves recent application logs to a file and opens it in your default text viewer. Useful when reporting bugs.

**Buffer size** is how many log lines are kept in memory.

**Log level** controls how much is captured. Switch to DEBUG when you're trying to reproduce a problem.`)

	bufferRow := container.NewHBox(
		widget.NewLabel("Log buffer size:"),
		numericEntry(bufferEntry),
		widget.NewLabel("lines"),
		info,
	)

	levelSelect := widget.NewSelect([]string{"DEBUG", "INFO", "WARN", "ERROR"}, func(v string) {
		settingsService.UpdateSettings(func(s *settings.Settings) {
			s.LogLevel = v
		})
		slog.Info("Updated setting: LogLevel", "value", v)
	})
	switch current.LogLevel {
	case "DEBUG", "INFO", "WARN", "ERROR":
		levelSelect.SetSelected(current.LogLevel)
	default:
		levelSelect.SetSelected("INFO")
	}
	levelRow := container.NewHBox(
		widget.NewLabel("Log level:"),
		levelSelect,
	)

	// Wrap the button in an HBox so it sizes to its content instead of
	// stretching to the card's full width (which VBox children do by
	// default).
	buttonRow := container.NewHBox(openButton)

	return widget.NewCard("Diagnostics", "", container.NewVBox(bufferRow, levelRow, buttonRow))
}
