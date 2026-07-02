package sso

import (
	"context"
	"time"
)

// Status represents the status of an SSO session
type Status int

const (
	StatusLoading Status = iota
	StatusActive
	StatusExpiringSoon
	StatusExpired
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusLoading:
		return "Loading"
	case StatusActive:
		return "Active"
	case StatusExpiringSoon:
		return "Expiring Soon"
	case StatusExpired:
		return "Expired"
	case StatusError:
		return "Error"
	default:
		return "Unknown"
	}
}

// Provider represents an SSO provider that can manage sessions.
type Provider interface {
	ID() string
	Name() string

	ActiveSessions(ctx context.Context) ([]*Session, error)
	Login(ctx context.Context, session *Session) error
}

// CacheChecker is an optional interface that providers can implement
// to expose cache checking capabilities.
type CacheChecker interface {
	SsoTokenCacheExists(session *Session) bool
}

// Session represents a user-friendly SSO session with display information
type Session struct {
	DisplayName string
	Provider    Provider
	Status      Status
	ExpiresAt   time.Time
	Metadata    map[string]string

	// Internal session ID for provider operations
	SessionID string
}

// NewSession creates a new Session
func NewSession(sessionID, displayName string, provider Provider, status Status, expiresAt time.Time) *Session {
	return &Session{
		SessionID:   sessionID,
		DisplayName: displayName,
		Provider:    provider,
		Status:      status,
		ExpiresAt:   expiresAt,
		Metadata:    make(map[string]string),
	}
}

// Login performs login for this session
func (s *Session) Login(ctx context.Context, session *Session) error {
	return s.Provider.Login(ctx, session)
}

// TimeUntilExpiry returns the time until the session expires
func (s *Session) TimeUntilExpiry() time.Duration {
	return time.Until(s.ExpiresAt)
}

func (s *Session) String() string {
	return s.DisplayName + " - " + s.Status.String() + " - Expires: " + s.ExpiresAt.Format("2006-01-02 15:04:05")
}
