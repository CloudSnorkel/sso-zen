package notify

import (
	"log/slog"
	"runtime"

	"fyne.io/fyne/v2"
)

// LoginRequest is what notify emits when the user clicks the Login
// action on a toast. The handler (typically main) finds the session,
// runs whatever login flow is appropriate, and sends the outcome back
// on Done. After receiving the result, notify shows the appropriate
// follow-up toast.
type LoginRequest struct {
	SessionID string
	Done      chan<- error
}

// Service is the application's user-notification dispatcher. It picks
// the right platform backend at construction time and routes every
// Notification through it.
type Service struct {
	app             fyne.App
	windowsNotifier *windowsService

	// loginRequests carries Login-button clicks from the Windows toast
	// activation handler out to whoever's wired up to fulfil them
	// (main, via LoginRequests()). Buffer of one so a slow consumer
	// doesn't deadlock the OS thread the toast callback runs on; if it
	// genuinely fills up we drop the click rather than block.
	loginRequests chan LoginRequest
}

func NewService(app fyne.App) *Service {
	s := &Service{
		app:           app,
		loginRequests: make(chan LoginRequest, 1),
	}
	if runtime.GOOS == "windows" {
		s.windowsNotifier = newWindowsService(s)
	}
	return s
}

// LoginRequests returns the channel of pending Login-button clicks.
// Consumers read each LoginRequest, perform whatever login flow they
// own, and send the outcome (nil for success, an error otherwise) on
// LoginRequest.Done.
func (s *Service) LoginRequests() <-chan LoginRequest {
	return s.loginRequests
}

func (s *Service) Notify(n Notification) {
	if s.windowsNotifier != nil {
		s.windowsNotifier.notify(n)
		return
	}
	slog.Info("Showing notification", "title", n.Title, "message", n.Message)
	s.app.SendNotification(fyne.NewNotification(n.Title, n.Message))
}
