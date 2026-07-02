package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
)

// Settings represents the application settings
type Settings struct {
	// StartWithComputer enables autostart on login
	StartWithComputer bool

	// ExpiryNotificationThreshold is the time before expiry to show notification
	ExpiryNotificationThreshold time.Duration

	// NotifyWhenExpiring enables expiring soon notifications
	NotifyWhenExpiring bool

	// NotifyWhenExpired controls the "Session Expired" toast that fires
	// when a session transitions Active/ExpiringSoon -> Expired. With
	// one row per role (D1f) this can fire for many rows at once,
	// especially when role creds cluster on expiry; users who rely on
	// CSM to catch expiry mid-command can mute it without losing that
	// reactive signal.
	NotifyWhenExpired bool

	// ProbeNearExpiry enables an active probe (sts:GetCallerIdentity) near
	// expiry to confirm whether the cached expiresAt still matches reality
	// before firing an "Expiring Soon" notification. Off by default: the
	// probe forces the SDK to refresh credentials, which writes fresh
	// working creds to ~/.aws/sso/cache and ~/.aws/cli/cache during idle
	// time. That weakens a small defense-in-depth layer against malware
	// reading those files.
	ProbeNearExpiry bool

	// LogBufferLines caps the in-memory log ring the debug Log window
	// reads from. The console handler still receives every line; this
	// only bounds how far back the window can scroll. Default is set
	// in New (1000). Zero or negative disables in-memory capture (Log
	// window will show nothing).
	LogBufferLines int

	// LogLevel is the slog level filter applied to both stdout and the
	// in-memory ring. One of "DEBUG", "INFO", "WARN", "ERROR".
	// Anything unrecognised (including the empty string from older
	// settings.json files) is treated as "INFO". The SSO_ZEN_DEBUG=1
	// env var still forces DEBUG at boot for developer convenience,
	// regardless of this setting.
	LogLevel string

	// Providers holds per-provider settings as raw JSON, keyed by
	// provider ID. Each provider package owns its slot — the global
	// settings code never inspects the bytes.
	Providers map[string]json.RawMessage `json:",omitempty"`
}

// Store manages application settings
type Store struct {
	app      fyne.App
	settings *Settings
	changes  chan *Settings
	firstRun bool
}

// New creates a new settings store. Changes() returns a channel that
// receives the current settings each time UpdateSettings persists.
// Buffer of one with non-blocking send so a slow consumer never blocks
// the writer; the consumer always sees the latest state on next read.
func New(app fyne.App) *Store {
	ss := &Store{

		app: app,
		settings: &Settings{
			// Default settings
			StartWithComputer:           false,
			ExpiryNotificationThreshold: 5 * time.Minute,
			NotifyWhenExpiring:          true,
			NotifyWhenExpired:           true,
			ProbeNearExpiry:             false,
			LogBufferLines:              1000,
			LogLevel:                    "INFO",
			Providers:                   make(map[string]json.RawMessage),
		},
		changes: make(chan *Settings, 1),
	}

	// Load settings
	if err := ss.Load(); err != nil {
		slog.Warn("Failed to load settings, using defaults", "error", err)
	}

	return ss
}

// Settings returns the current settings
func (ss *Store) Settings() *Settings {
	return ss.settings
}

// Changes returns a channel that emits the current settings each time
// UpdateSettings persists. Buffered with non-blocking send: if the
// consumer is slow the writer drops the intermediate value, since the
// channel only needs to deliver the latest state.
func (ss *Store) Changes() <-chan *Settings {
	return ss.changes
}

// ProviderSettings loads provider-scoped settings into out (which must
// be a pointer to a struct). Returns false if no settings have been
// stored for that ID yet so the caller can apply its own defaults.
func (ss *Store) ProviderSettings(providerID string, out interface{}) (bool, error) {
	raw, ok := ss.settings.Providers[providerID]
	if !ok || len(raw) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("unmarshal provider settings for %s: %w", providerID, err)
	}
	return true, nil
}

// UpdateProviderSettings marshals v and persists it under providerID,
// then emits on Changes() like UpdateSettings does.
func (ss *Store) UpdateProviderSettings(providerID string, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal provider settings for %s: %w", providerID, err)
	}
	if ss.settings.Providers == nil {
		ss.settings.Providers = make(map[string]json.RawMessage)
	}
	ss.settings.Providers[providerID] = raw
	if err := ss.Save(); err != nil {
		return err
	}
	select {
	case ss.changes <- ss.settings:
	default:
	}
	return nil
}

// UpdateSettings updates the settings
func (ss *Store) UpdateSettings(update func(*Settings)) error {
	update(ss.settings)
	if err := ss.Save(); err != nil {
		return err
	}
	select {
	case ss.changes <- ss.settings:
	default:
		// Consumer hasn't drained the previous change yet; they'll
		// see this state on the next read either way.
	}
	return nil
}

// Load loads settings from disk
func (ss *Store) Load() error {
	configDir, err := ss.getConfigDir()
	if err != nil {
		return err
	}

	settingsFile := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No settings file yet, use defaults
			ss.firstRun = true
			return nil
		}
		return fmt.Errorf("failed to read settings file: %w", err)
	}

	if err := json.Unmarshal(data, ss.settings); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	slog.Info("Settings loaded successfully")
	return nil
}

// IsFirstRun returns true if this is the first time the app is started
func (ss *Store) IsFirstRun() bool {
	return ss.firstRun
}

// Save saves settings to disk
func (ss *Store) Save() error {
	configDir, err := ss.getConfigDir()
	if err != nil {
		return err
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	settingsFile := filepath.Join(configDir, "settings.json")
	data, err := json.MarshalIndent(ss.settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	slog.Info("Settings saved successfully")
	return nil
}

// getConfigDir returns the configuration directory for the app
func (ss *Store) getConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	// Use app ID from Fyne for config directory
	appID := ss.app.Metadata().ID
	if appID == "" {
		appID = "sso-zen"
	}

	return filepath.Join(homeDir, ".config", appID), nil
}
