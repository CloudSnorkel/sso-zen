package notify

import (
	"fmt"
	"time"

	"sso-zen/sso"
)

// SessionExpired builds the standard "session expired" notification for
// the given session. Use with Notifier.Notify.
func SessionExpired(s *sso.Session) Notification {
	return Notification{
		Title:          "SSO Session Expired",
		Message:        fmt.Sprintf("Session for %s has expired.", s.DisplayName),
		LoginSessionID: s.SessionID,
	}
}

// SessionExpiringSoon builds the standard "session expiring soon"
// notification for the given session.
func SessionExpiringSoon(s *sso.Session, in time.Duration) Notification {
	return Notification{
		Title:          "SSO Session Expiring Soon",
		Message:        fmt.Sprintf("Session for %s expires in %s.", s.DisplayName, formatDuration(in)),
		LoginSessionID: s.SessionID,
	}
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0f seconds", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, minutes)
}
