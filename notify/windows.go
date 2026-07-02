//go:build windows

package notify

import (
	"fmt"
	"log/slog"
	"strings"

	"git.sr.ht/~jackmordaunt/go-toast"
)

// windowsService renders Notifications as native Windows toasts. When
// Notification.LoginSessionID is set, it attaches a Login + Dismiss
// action pair whose Foreground arguments dispatch back through the
// in-process activation handler (not Windows' protocol-handler URI
// path — that would need a registered scheme we don't ship).
type windowsService struct {
	parent *Service
}

func newWindowsService(parent *Service) *windowsService {
	w := &windowsService{parent: parent}
	toast.SetActivationCallback(w.handleActivation)
	return w
}

func (w *windowsService) notify(n Notification) {
	slog.Info("Showing toast notification",
		"title", n.Title,
		"message", n.Message,
		"loginSessionID", n.LoginSessionID,
	)
	t := toast.Notification{AppID: "SSO Zen", Title: n.Title, Body: n.Message}
	if n.LoginSessionID != "" {
		// Foreground (not Protocol) so the click dispatches into our
		// in-process activation handler instead of asking Windows to
		// launch a "login:" URL via a protocol handler that isn't
		// registered.
		t.Actions = append(t.Actions, toast.Action{
			Type:      toast.Foreground,
			Content:   "Login",
			Arguments: fmt.Sprintf("login:%s", n.LoginSessionID),
		})
		t.Actions = append(t.Actions, toast.Action{
			Type:      toast.Foreground,
			Content:   "Dismiss",
			Arguments: "dismiss",
		})
	}
	if err := t.Push(); err != nil {
		slog.Error("Failed to send toast notification", "error", err)
	}
}

func (w *windowsService) handleActivation(args string, data []toast.UserData) {
	slog.Info("Toast notification activated", "args", args, "data", data)

	if args == "dismiss" {
		slog.Debug("Toast notification dismissed")
		return
	}

	if !strings.HasPrefix(args, "login:") {
		return
	}
	sessionID := strings.TrimPrefix(args, "login:")
	if sessionID == "" {
		slog.Error("Invalid login action", "args", args)
		return
	}
	slog.Info("Login action triggered", "sessionID", sessionID)

	go func() {
		done := make(chan error, 1)
		select {
		case w.parent.loginRequests <- LoginRequest{SessionID: sessionID, Done: done}:
		default:
			// No consumer listening (or the buffer is full). Don't
			// block the OS thread the toast callback ran on; drop the
			// click and log.
			slog.Warn("LoginRequests channel full or unread; dropping toast click",
				"sessionID", sessionID)
			return
		}
		err := <-done
		if err != nil {
			slog.Error("Login flow failed", "error", err)
			w.notify(Notification{
				Title:   "Login Failed",
				Message: fmt.Sprintf("Failed to login to %s: %s", sessionID, err.Error()),
			})
			return
		}
		slog.Info("Login flow succeeded", "sessionID", sessionID)
		w.notify(Notification{
			Title:   "Login Successful",
			Message: fmt.Sprintf("Successfully logged into %s", sessionID),
		})
	}()
}
