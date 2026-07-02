package notify

// Notification is the value type the rest of the app passes to Notifier.
// LoginSessionID, when set, signals the implementation to attach an
// interactive "Login" action (currently used by the Windows toast
// backend; Fyne falls back to a plain notification).
type Notification struct {
	Title          string
	Message        string
	LoginSessionID string
}

// Notifier shows a user-visible notification.
type Notifier interface {
	Notify(Notification)
}
