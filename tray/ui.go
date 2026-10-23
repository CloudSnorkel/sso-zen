package tray

import (
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"sso-zen/notify"
	"sso-zen/sso"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"
)

// SessionRefresher is the slice of the session monitor the tray uses
// (manual-refresh button + post-login refresh). Declared here per
// "interfaces live with the consumer".
type SessionRefresher interface {
	Refresh(ctx context.Context) error
}

// Notifier is the slice of notification behaviour the tray uses (post-
// action toasts).
type Notifier interface {
	Notify(notify.Notification)
}

// IconProvider is implemented by providers that ship a custom icon for
// their session rows. Providers that don't fall back to
// theme.AccountIcon().
type IconProvider interface {
	Icon() fyne.Resource
}

// LogWindow is the slice of the debug log window the tray needs — just
// show. The host owns the real implementation (ui.LogWindow).
type LogWindow interface {
	Show()
}

// UI manages the system tray
type UI struct {
	app               fyne.App
	window            fyne.Window
	sessionMonitoring SessionRefresher
	notifier          Notifier
	logWindow         LogWindow

	// UI state
	sessionsContainer *fyne.Container
	sessions          []*sso.Session
	sessionsMutex     sync.RWMutex

	// View management
	currentView        *fyne.Container // The main content view
	settingsContainer  *fyne.Container // Settings view
	backToSessionsFunc func()          // Function to return to sessions view

	// Event handling
	ctx context.Context

	// Quit coordination
	quitChan chan struct{}

	// UI refresh management
	isWindowVisible bool
	refreshTimer    *time.Timer
	refreshMutex    sync.Mutex

	// View state flags
	isShowingSettings bool
}

// New creates and initialises the tray. The window, event handlers and
// system-tray hooks are wired immediately; ctx drives shutdown — when
// ctx is cancelled the UI stops its refresh ticker and releases
// resources via the OnClosed paths Fyne already runs.
func New(
	ctx context.Context,

	app fyne.App,
	sessionMonitoring SessionRefresher,
	notifier Notifier,
	logWindow LogWindow,
) *UI {
	ui := &UI{

		app:               app,
		ctx:               ctx,
		sessionMonitoring: sessionMonitoring,
		notifier:          notifier,
		logWindow:         logWindow,
		sessions:          make([]*sso.Session, 0),
		quitChan:          make(chan struct{}),
	}

	ui.window = app.NewWindow("SSO Zen")
	ui.window.SetIcon(app.Icon())
	ui.window.Resize(fyne.NewSize(800, 600))
	ui.window.CenterOnScreen()

	ui.createUI()
	ui.setupEventHandlers()
	ui.setupSystemTray()

	// Stop the refresh ticker when ctx is cancelled. Fyne's app lifecycle
	// handles window teardown.
	go func() {
		<-ctx.Done()
		ui.stopUIRefreshTimer()
	}()

	slog.Info("Tray UI ready")
	return ui
}

// UpdateSessions updates the sessions in the UI
func (ts *UI) UpdateSessions(sessions []*sso.Session) {
	slog.Info("Updating sessions in UI", "count", len(sessions))

	ts.sessionsMutex.Lock()
	ts.sessions = sessions
	ts.sessionsMutex.Unlock()

	// Update the UI on the main thread
	fyne.Do(func() {
		ts.updateSessionsContainer()
	})
}

// updateSessionsContainer updates the sessions container in the UI
func (ts *UI) updateSessionsContainer() {
	ts.sessionsMutex.RLock()
	// Group rows by their SSO session (when the provider exposes one),
	// so multiple profiles under the same SSO session display together
	// inside a collapsible section. Rows without an "SSOSession"
	// metadata land in a trailing flat list below the sections.
	type group struct {
		name     string
		sessions []*sso.Session
	}
	groupsByName := make(map[string]*group)
	var groupOrder []string
	for _, s := range ts.sessions {
		name := s.Metadata["SSO Session"]
		g, ok := groupsByName[name]
		if !ok {
			g = &group{name: name}
			groupsByName[name] = g
			groupOrder = append(groupOrder, name)
		}
		g.sessions = append(g.sessions, s)
	}
	ts.sessionsMutex.RUnlock()

	// Sort: named groups alphabetically, standalone group (empty name) last.
	sort.Slice(groupOrder, func(i, j int) bool {
		if groupOrder[i] == "" {
			return false
		}
		if groupOrder[j] == "" {
			return true
		}
		return groupOrder[i] < groupOrder[j]
	})

	var accordion *widget.Accordion
	var standaloneSessions []*sso.Session

	for _, name := range groupOrder {
		g := groupsByName[name]
		sort.Slice(g.sessions, func(i, j int) bool {
			return g.sessions[i].DisplayName < g.sessions[j].DisplayName
		})

		if name == "" {
			standaloneSessions = g.sessions
			continue
		}

		rowsBox := container.NewVBox()
		for i, s := range g.sessions {
			rowsBox.Add(ts.createSessionRow(s, i%2 == 0))
		}

		if accordion == nil {
			accordion = widget.NewAccordion()
			accordion.MultiOpen = true
		}
		item := widget.NewAccordionItem(name, rowsBox)
		item.Open = true
		accordion.Append(item)
	}

	// When named groups exist, also wrap standalone rows in their own
	// section so they can't be mistaken for the last named group's
	// content. With only standalone rows there's nothing to confuse
	// them with — render flat.
	if len(standaloneSessions) > 0 && accordion != nil {
		rowsBox := container.NewVBox()
		for i, s := range standaloneSessions {
			rowsBox.Add(ts.createSessionRow(s, i%2 == 0))
		}
		item := widget.NewAccordionItem("No session", rowsBox)
		item.Open = true
		accordion.Append(item)
	}

	ts.sessionsContainer.RemoveAll()
	if accordion != nil {
		ts.sessionsContainer.Add(accordion)
	} else {
		for i, s := range standaloneSessions {
			ts.sessionsContainer.Add(ts.createSessionRow(s, i%2 == 0))
		}
	}

	// If sessions view is currently shown, ensure layout is up to date.
	// Avoid changing content while settings view is visible.
	if !ts.isShowingSettings {
		ts.window.SetContent(ts.createMainContent())
	}
}

// ShowWindow shows the main window
func (ts *UI) ShowWindow() {
	fyne.Do(func() {
		ts.window.Show()
		restore(ts.window)
		ts.window.RequestFocus()
		ts.startUIRefreshTimer()
	})
}

// QuitChannel returns the quit channel for listening to quit requests
func (ts *UI) QuitChannel() <-chan struct{} {
	return ts.quitChan
}

// HideWindow hides the main window
func (ts *UI) HideWindow() {
	fyne.Do(func() {
		ts.window.Hide()
		ts.stopUIRefreshTimer()
	})
}

// createMainContent creates the main window content
func (ts *UI) createMainContent() fyne.CanvasObject {
	// Create header with icon and title
	headerIcon := widget.NewIcon(theme.ComputerIcon())
	headerTitle := widget.NewLabel("SSO Sessions")
	headerTitle.TextStyle.Bold = true

	headerContainer := container.NewHBox(
		headerIcon,
		widget.NewSeparator(),
		headerTitle,
		layout.NewSpacer(),
	)

	// Create footer with action buttons
	refreshAllButton := widget.NewButtonWithIcon("Refresh All", theme.ViewRefreshIcon(), func() {
		ts.refreshAllSessions()
	})
	refreshAllButton.Importance = widget.MediumImportance

	settingsButton := widget.NewButtonWithIcon("Settings", theme.SettingsIcon(), func() {
		ts.showSettings()
	})
	settingsButton.Importance = widget.LowImportance

	quitButton := widget.NewButtonWithIcon("Quit", theme.CancelIcon(), func() {
		slog.Info("Quit requested from UI")
		select {
		case ts.quitChan <- struct{}{}:
			// Quit signal sent
		default:
			// Channel is full, ignore
		}
	})
	quitButton.Importance = widget.DangerImportance

	buttonContainer := container.NewHBox(
		refreshAllButton,
		settingsButton,
		layout.NewSpacer(),
		quitButton,
	)

	// Create scrollable container for the sessions
	scrollContainer := container.NewVScroll(ts.sessionsContainer)

	// Create main layout - store reference for view switching
	ts.currentView = container.NewBorder(
		headerContainer, // Top
		buttonContainer, // Bottom
		nil,             // Left
		nil,             // Right
		scrollContainer, // Center - scrollable grid
	)

	return ts.currentView
}

// createUI creates the main UI
func (ts *UI) createUI() {
	// Initialize sessions container as vertical container for table rows
	ts.sessionsContainer = container.NewVBox()

	// Set the initial content
	ts.window.SetContent(ts.createMainContent())
}

// createSessionRow creates a single table row for a session
func (ts *UI) createSessionRow(session *sso.Session, isEven bool) fyne.CanvasObject {
	// Create provider icon
	providerIcon := widget.NewIcon(providerIcon(session.Provider))

	// Create session name label
	sessionNameLabel := widget.NewLabel(session.DisplayName)
	sessionNameLabel.TextStyle.Bold = true

	// Create expiry time label with appropriate color
	expiryText := ts.formatExpiryTime(session.ExpiresAt, session.Status)
	expiryLabel := widget.NewLabel(expiryText)

	// Set color based on status
	if session.Status == sso.StatusExpired {
		expiryLabel.Importance = widget.DangerImportance
	} else if session.Status == sso.StatusExpiringSoon {
		expiryLabel.Importance = widget.WarningImportance
	}

	// Create info button for role/account details
	infoButton := widget.NewButtonWithIcon("", theme.InfoIcon(), func() {
		ts.showSessionDetails(session)
	})
	infoButton.Importance = widget.LowImportance

	// Create login button
	loginButton := widget.NewButtonWithIcon("Login", theme.LoginIcon(), func() {
		ts.handleLogin(session)
	})
	loginButton.Importance = widget.MediumImportance

	// Set alternating background color
	var bgColor color.NRGBA
	if isEven {
		bgColor = color.NRGBA{240, 240, 240, 255} // Light gray
	} else {
		bgColor = color.NRGBA{255, 255, 255, 255} // White
	}

	// Create background rectangle
	rect := canvas.NewRectangle(bgColor)
	rect.StrokeColor = color.NRGBA{200, 200, 200, 255}
	rect.StrokeWidth = 0.5

	// Create the row content with consistent spacing
	rowContent := container.NewHBox(
		providerIcon,        // Provider icon
		widget.NewLabel(""), // Small spacer
		container.NewWithoutLayout(sessionNameLabel), // Session name in fixed container
		widget.NewLabel(""),                          // Small spacer
		layout.NewSpacer(),                           // Flexible spacer
		expiryLabel,                                  // Expiry text
		widget.NewLabel(""),                          // Small spacer
		infoButton,                                   // Info button
		loginButton,                                  // Login button (reduced spacing)
	)

	// Create the row with background
	row := container.NewStack(
		rect,
		container.NewPadded(rowContent),
	)

	// Set minimum height for consistent row height
	row.Resize(fyne.NewSize(0, 40))

	// Return the row directly - no hover functionality
	return row
}

// providerIcon returns the provider's icon when it implements
// IconProvider, falling back to theme.AccountIcon otherwise.
func providerIcon(p sso.Provider) fyne.Resource {
	if ip, ok := p.(IconProvider); ok {
		if icon := ip.Icon(); icon != nil {
			return icon
		}
	}
	return theme.AccountIcon()
}

// showSessionDetails opens a modal popup with this session's status
// and provider metadata. Matches the chrome of the settings info
// popups — bold header, scrollable body, Close button at the bottom
// right, sized responsively against the parent canvas.
func (ts *UI) showSessionDetails(session *sso.Session) {
	canvas := ts.window.Canvas()
	if canvas == nil {
		return
	}

	header := widget.NewLabelWithStyle(
		"Session Details — "+session.DisplayName,
		fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true},
	)

	mainDetails := fmt.Sprintf("Provider: %s\nStatus: %s\nExpires %s",
		session.Provider.Name(),
		session.Status.String(),
		session.ExpiresAt.Format("2006-01-02 15:04:05"),
	)
	mainLabel := widget.NewLabel(mainDetails)
	mainLabel.Wrapping = fyne.TextWrapWord

	var metadataContent fyne.CanvasObject
	if len(session.Metadata) > 0 {
		metadataContent = ts.createMetadataTable(session.Metadata)
	} else {
		metadataContent = widget.NewLabel("No additional details available")
	}

	body := container.NewBorder(
		container.NewVBox(
			mainLabel,
			widget.NewSeparator(),
			widget.NewLabel("Details:"),
		),
		nil, nil, nil,
		metadataContent,
	)

	var popup *widget.PopUp
	closeBtn := widget.NewButton("Close", func() { popup.Hide() })
	closeRow := container.NewHBox(layout.NewSpacer(), closeBtn)

	content := container.NewBorder(header, closeRow, nil, nil, container.NewVScroll(body))
	// Non-modal so a tap outside dismisses; Close button is still there
	// for keyboard / touch users. NewPopUp lands at the canvas origin by
	// default — ShowAtPosition with manual centering matches the modal
	// look without the modal lockout.
	popup = widget.NewPopUp(container.NewPadded(content), canvas)
	size := canvas.Size()
	popupSize := fyne.NewSize(min(size.Width-40, 600), min(size.Height-80, 400))
	popup.Resize(popupSize)
	popup.ShowAtPosition(fyne.NewPos(
		(size.Width-popupSize.Width)/2,
		(size.Height-popupSize.Height)/2,
	))
}

// createMetadataTable creates a nicely formatted table for metadata
func (ts *UI) createMetadataTable(metadata map[string]string) fyne.CanvasObject {
	// Sort keys for consistent display
	var keys []string
	for key := range metadata {
		if !strings.HasPrefix(key, "_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	// Create table data structure
	tableData := make([][]string, len(keys))
	for i, key := range keys {
		value := metadata[key]
		tableData[i] = []string{key, value}
	}

	// Calculate maximum widths and row heights upfront
	var maxKeyWidth, maxValueWidth float32
	rowHeights := make([]float32, len(tableData))

	// Create temporary label to measure text sizes
	tempLabel := widget.NewLabel("")
	tempLabel.Wrapping = fyne.TextWrapOff
	tempLabel.Truncation = fyne.TextTruncateOff

	// Measure all text sizes
	for i, row := range tableData {
		var maxRowHeight float32

		// Measure key width and height
		tempLabel.TextStyle.Bold = true
		tempLabel.SetText(row[0] + ":")
		if width := tempLabel.MinSize().Width; width > maxKeyWidth {
			maxKeyWidth = width
		}
		if height := tempLabel.MinSize().Height; height > maxRowHeight {
			maxRowHeight = height
		}

		// Measure value width and height
		tempLabel.TextStyle.Bold = false
		valueText := strings.Join(strings.Split(row[1], ","), "\n")
		tempLabel.SetText(valueText)
		if width := tempLabel.MinSize().Width; width > maxValueWidth {
			maxValueWidth = width
		}
		if height := tempLabel.MinSize().Height; height > maxRowHeight {
			maxRowHeight = height
		}

		rowHeights[i] = maxRowHeight
	}

	// Create table widget
	table := widget.NewTable(
		func() (int, int) {
			return len(tableData), 2 // rows, columns
		},
		func() fyne.CanvasObject {
			// Create cell template - key column will be bold, value column normal
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			if id.Row < len(tableData) && id.Col < len(tableData[id.Row]) {
				text := tableData[id.Row][id.Col]
				if id.Col == 0 {
					// Key column - make bold and add colon
					text = text + ":"
					label.TextStyle.Bold = true
					label.Wrapping = fyne.TextWrapOff
					label.Truncation = fyne.TextTruncateOff
				} else {
					// Value column - normal text
					label.TextStyle.Bold = false
					label.Wrapping = fyne.TextWrapOff
					label.Truncation = fyne.TextTruncateOff
					text = strings.Join(strings.Split(text, ","), "\n")
				}
				label.SetText(text)
			}
		},
	)

	// Set column widths based on pre-calculated maximum widths
	table.SetColumnWidth(0, maxKeyWidth)   // Key column
	table.SetColumnWidth(1, maxValueWidth) // Value column

	// Set row heights based on content
	for i, height := range rowHeights {
		table.SetRowHeight(i, height)
	}

	// Set minimum size to ensure the table expands properly
	table.Resize(fyne.NewSize(450, 200))

	return table
}

// formatExpiryTime formats the expiry time for display
func (ts *UI) formatExpiryTime(expiresAt time.Time, status sso.Status) string {
	// If session status is expired, show "Expired" regardless of expiry time
	if status == sso.StatusExpired {
		return "Expired"
	}

	if expiresAt.IsZero() {
		return "Expires: Unknown"
	}

	now := time.Now()
	if expiresAt.Before(now) {
		duration := now.Sub(expiresAt)
		return fmt.Sprintf("Expired %s ago", ts.formatDuration(duration))
	}

	duration := expiresAt.Sub(now)
	return fmt.Sprintf("Expires in %s", ts.formatDuration(duration))
}

// formatDuration formats a duration into human-readable format
func (ts *UI) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}

	if d < time.Hour {
		minutes := int(d.Minutes())
		if minutes == 1 {
			return "1m"
		}
		return fmt.Sprintf("%dm", minutes)
	}

	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60

		if hours == 1 {
			if minutes == 0 {
				return "1h"
			}
			return fmt.Sprintf("1h %dm", minutes)
		}

		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}

	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24

	if days == 1 {
		if hours == 0 {
			return "1d"
		}
		return fmt.Sprintf("1d %dh", hours)
	}

	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}

// handleLogin handles login button clicks
func (ts *UI) handleLogin(session *sso.Session) {
	slog.Info("Login requested", "displayName", session.DisplayName, "provider", session.Provider.ID())

	// Run login in a goroutine to avoid blocking the UI
	go func() {
		ctx := ts.ctx

		if err := session.Login(ctx, session); err != nil {
			slog.Error("Login failed", "displayName", session.DisplayName, "error", err)
			go ts.notifier.Notify(notify.Notification{Title: "Login Failed", Message: fmt.Sprintf("Failed to login to %s: %s", session.DisplayName, err.Error())})
		} else {
			slog.Info("Login successful", "displayName", session.DisplayName)
			go ts.notifier.Notify(notify.Notification{Title: "Login Successful", Message: fmt.Sprintf("Successfully logged into %s", session.DisplayName)})
			// Refresh sessions after a successful login
			if err := ts.sessionMonitoring.Refresh(ctx); err != nil {
				slog.Error("Post-login refresh failed", "displayName", session.DisplayName, "error", err)
			}
		}
	}()
}

// refreshAllSessions refreshes all sessions by calling the session monitoring service
func (ts *UI) refreshAllSessions() {
	slog.Info("Refreshing all sessions from tray")

	// Use base context; reading files is quick
	if err := ts.sessionMonitoring.Refresh(ts.ctx); err != nil {
		slog.Error("Failed to refresh sessions", "error", err)
		// Show error notification
		go ts.notifier.Notify(notify.Notification{Title: "Refresh Failed", Message: "Failed to refresh sessions: " + err.Error()})
	}
}

// setupEventHandlers sets up event handlers
func (ts *UI) setupEventHandlers() {
	// Handle window close - hide instead of quit
	ts.window.SetCloseIntercept(func() {
		ts.window.Hide()
		ts.stopUIRefreshTimer()
	})

	// No keyboard event handling - main.go handles Ctrl+C via signal handling
}

// setupSystemTray sets up the system tray using Fyne's built-in functionality
func (ts *UI) setupSystemTray() {
	// Check if the app supports desktop features (system tray)
	if desk, ok := ts.app.(desktop.App); ok {
		// Set the system tray icon
		desk.SetSystemTrayIcon(ts.app.Icon())

		// Create the context menu
		menu := fyne.NewMenu("SSO Zen",
			fyne.NewMenuItem("Show Window", func() {
				ts.ShowWindow()
			}),
			fyne.NewMenuItem("Refresh Sessions", func() {
				ts.refreshAllSessions()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Settings", func() {
				ts.showSettings()
			}),
			fyne.NewMenuItem("Open Log", func() {
				if ts.logWindow != nil {
					ts.logWindow.Show()
				}
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", func() {
				slog.Info("Quit requested from tray menu")
				select {
				case ts.quitChan <- struct{}{}:
					// Quit signal sent
				default:
					// Channel is full, ignore
				}
			}),
		)
		desk.SetSystemTrayMenu(menu)
		// Not desk.SetSystemTrayWindow: its tap handler only calls Show, which
		// doesn't un-minimize the window, and it replaces our close intercept
		// so the refresh timer never stops.
		systray.SetOnTapped(ts.ShowWindow)
	} else {
		slog.Warn("Desktop features not supported - system tray will not be available")
	}
}

// startUIRefreshTimer starts the UI refresh timer to update expiry times every minute
func (ts *UI) startUIRefreshTimer() {
	ts.refreshMutex.Lock()
	defer ts.refreshMutex.Unlock()

	// Don't start if already running
	if ts.refreshTimer != nil {
		return
	}

	ts.isWindowVisible = true

	// Start the timer to refresh UI every minute
	ts.refreshTimer = time.AfterFunc(time.Minute, func() {
		ts.refreshUIExpiryTimes()
	})
}

// stopUIRefreshTimer stops the UI refresh timer
func (ts *UI) stopUIRefreshTimer() {
	ts.refreshMutex.Lock()
	defer ts.refreshMutex.Unlock()

	if ts.refreshTimer != nil {
		ts.refreshTimer.Stop()
		ts.refreshTimer = nil
		ts.isWindowVisible = false
	}
}

// refreshUIExpiryTimes refreshes the UI to update expiry times
func (ts *UI) refreshUIExpiryTimes() {
	ts.refreshMutex.Lock()
	defer ts.refreshMutex.Unlock()

	// Check if window is still visible and timer should continue
	if !ts.isWindowVisible || ts.refreshTimer == nil {
		return
	}

	// Update the UI on the main thread
	fyne.Do(func() {
		ts.updateSessionsContainer()
	})

	// Schedule the next refresh
	ts.refreshTimer = time.AfterFunc(time.Minute, func() {
		ts.refreshUIExpiryTimes()
	})
}

// SetSettingsContainer sets the settings container
func (ts *UI) SetSettingsContainer(settingsContainer *fyne.Container) {
	ts.settingsContainer = settingsContainer
}

// showSettings shows the settings view in the main window
func (ts *UI) showSettings() {
	if ts.settingsContainer == nil {
		slog.Warn("Settings container not initialized")
		return
	}
	fyne.Do(func() {
		ts.stopUIRefreshTimer()
		ts.isShowingSettings = true
		ts.window.SetContent(ts.settingsContainer)
	})
}

// ShowSettings exposes showing the settings view publicly
func (ts *UI) ShowSettings() {
	ts.showSettings()
}

// ShowSessions shows the sessions view in the main window
func (ts *UI) ShowSessions() {
	if ts.currentView == nil {
		slog.Warn("Sessions view not initialized")
		return
	}
	fyne.Do(func() {
		ts.startUIRefreshTimer()
		ts.isShowingSettings = false
		ts.window.SetContent(ts.currentView)
	})

	// Set up the back function for future use
	if ts.backToSessionsFunc == nil {
		ts.backToSessionsFunc = ts.ShowSessions
	}
}
