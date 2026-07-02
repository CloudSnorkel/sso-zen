package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sso-zen/notify"
	"sso-zen/sso"

	"gopkg.in/ini.v1"
)

// csmNotificationCache throttles duplicate notifications per key within
// a fixed window. now is injectable for tests; defaults to time.Now.
type csmNotificationCache struct {
	mu           sync.Mutex
	lastNotified map[string]time.Time
	window       time.Duration
	now          func() time.Time
}

func newCsmNotificationCache(window time.Duration) *csmNotificationCache {
	return &csmNotificationCache{
		lastNotified: make(map[string]time.Time),
		window:       window,
		now:          time.Now,
	}
}

// ShouldNotify returns true if a notification should be sent for the given key now
func (c *csmNotificationCache) ShouldNotify(key string) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if last, ok := c.lastNotified[key]; ok {
		if last.Add(c.window).After(now) {
			return false
		}
	}
	c.lastNotified[key] = now
	return true
}

// MonitorLive runs AWS CSM monitoring until ctx cancels. Listens for
// per-profile CSM events emitted by aws CLI and fires "Session Expired"
// notifications when it sees credential failures. Implements the
// LiveMonitor capability the host runs at startup.
//
// The session set is captured from the AWS provider's own awsSessions
// map at call time; the caller (main) restarts MonitorLive whenever the
// profile set changes so listeners can be rewired.
func (p *Provider) MonitorLive(ctx context.Context, notifier notify.Notifier) error {
	if !p.Settings().EnableCSM {
		slog.Debug("AWS CSM disabled in settings; LiveMonitor idling until ctx cancels")
		<-ctx.Done()
		return ctx.Err()
	}

	slog.Info("Starting AWS CSM monitoring")

	p.sessionsMutex.RLock()
	awsSessions := make(map[string]*Session, len(p.awsSessions))
	for k, v := range p.awsSessions {
		awsSessions[k] = v
	}
	p.sessionsMutex.RUnlock()

	if len(awsSessions) == 0 {
		slog.Info("No active AWS sessions; CSM monitoring will idle until ctx cancels")
		<-ctx.Done()
		return ctx.Err()
	}

	profiles := make([]string, 0, len(awsSessions))
	sessionMap := make(map[string]*sso.Session)
	for _, a := range awsSessions {
		profile := strings.TrimSpace(a.Profile)
		if profile == "" {
			continue
		}
		profiles = append(profiles, profile)
		sessionMap[profile] = a.Session
	}

	if len(profiles) == 0 {
		slog.Info("No AWS sessions with profile data; CSM monitoring will idle")
		<-ctx.Done()
		return ctx.Err()
	}

	slog.Info("Starting CSM listeners for profiles", "profiles", profiles)

	// Create listeners for each profile
	listeners := make(map[string]*net.UDPConn)
	ports := make(map[string]int)

	defer func() {
		for profile, listener := range listeners {
			listener.Close()
			slog.Info("Closed CSM listener", "profile", profile)
		}
	}()

	for _, profile := range profiles {
		listener, port, err := createCsmListener()
		if err != nil {
			slog.Error("Failed to create listener for profile", "profile", profile, "error", err)
			continue
		}

		listeners[profile] = listener
		ports[profile] = port
		slog.Info("Created CSM listener", "profile", profile, "port", port)
	}

	if err := injectCsmConfigForProfiles(ports); err != nil {
		slog.Error("Failed to inject CSM config", "error", err)
		return err
	}

	defer func() {
		if err := RemoveCsmConfig(); err != nil {
			slog.Error("Failed to remove CSM config", "error", err)
		}
	}()

	cache := newCsmNotificationCache(10 * time.Minute)

	var wg sync.WaitGroup
	for profile, listener := range listeners {
		wg.Add(1)
		go func(profile string, listener *net.UDPConn) {
			defer wg.Done()
			p.monitorProfile(ctx, profile, listener, notifier, sessionMap[profile], cache)
		}(profile, listener)
	}

	<-ctx.Done()
	slog.Info("CSM monitoring stopped by context cancellation")

	for _, listener := range listeners {
		listener.Close()
	}

	wg.Wait()
	return ctx.Err()
}

// createCsmListener opens a loopback UDP listener for receiving CSM
// events. The caller picks per-profile bookkeeping; this just hands back
// the socket and the ephemeral port the OS assigned.
func createCsmListener() (*net.UDPConn, int, error) {
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}

	addr := listener.LocalAddr().(*net.UDPAddr)
	return listener.(*net.UDPConn), addr.Port, nil
}

// monitorProfile monitors CSM events for a specific profile
func (p *Provider) monitorProfile(ctx context.Context, profile string, listener *net.UDPConn, notifier notify.Notifier, session *sso.Session, cache *csmNotificationCache) {
	buffer := make([]byte, 65536) // 64KB buffer

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Set a timeout so we can check context periodically
			listener.SetReadDeadline(time.Now().Add(1 * time.Second))

			n, _, err := listener.ReadFrom(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// MonitorLive's deferred listener.Close() (fires when
				// ctx is cancelled by main's startLiveMonitor) races
				// with any in-flight ReadFrom — the read returns with
				// net.ErrClosed. That's the expected shutdown signal,
				// not an error worth logging. Bail quietly.
				if errors.Is(err, net.ErrClosed) {
					return
				}
				slog.Error("Error reading from UDP socket", "profile", profile, "error", err)
				continue
			}

			var csmEvent map[string]interface{}
			if err := json.Unmarshal(buffer[:n], &csmEvent); err != nil {
				slog.Error("Failed to parse CSM event JSON", "profile", profile, "error", err)
				continue
			}

			if session != nil {
				p.checkSsoTokenCacheExists(profile, session, notifier, cache)
			}

			eventType := getStringFromMap(csmEvent, "Type")
			service := getStringFromMap(csmEvent, "Service")
			operation := getStringFromMap(csmEvent, "Api")
			exception := getStringFromMap(csmEvent, "AwsException")
			attemptCount, _ := getNumberFromMap(csmEvent, "AttemptCount")

			slog.Debug("CSM event",
				"profile", profile,
				"type", eventType,
				"service", service,
				"api", operation,
				"exception", exception,
				"attemptCount", attemptCount,
			)

			switch {
			// New (aws CLI v2 post-update): when the SDK can't even
			// resolve credentials it emits a single ApiCall record for
			// the user-facing operation with AttemptCount=0 and no
			// exception fields — there was no HTTP attempt to record.
			// The CLI itself logs "Token has expired and refresh
			// failed"; CSM gives us no further detail beyond this
			// "tried but never got off the ground" shape.
			case eventType == "ApiCall" && attemptCount == 0:
				fireExpiredNotification(notifier, cache, profile, session, "credential resolution failed (AttemptCount=0)")

			// Older signal still present in some setups: a successful
			// HTTP refresh attempt that returned InvalidGrantException.
			case service == "SSO OIDC" && operation == "CreateToken" && exception == "InvalidGrantException":
				fireExpiredNotification(notifier, cache, profile, session, "SSO OIDC CreateToken InvalidGrantException")

			// SSO token cache missing — older CLI surfaced this with a
			// distinct exception text.
			case service == "SSO" && strings.Contains(exception, "Token") && strings.Contains(exception, "does not exist"):
				fireExpiredNotification(notifier, cache, profile, session, "SSO token cache missing")

			default:
				// Anything else: dump full event at Debug for diagnosis.
				prettyJSON, err := json.MarshalIndent(csmEvent, "", "  ")
				if err != nil {
					slog.Error("Failed to pretty print CSM event JSON", "profile", profile, "error", err)
					continue
				}
				slog.Debug("CSM event (full)", "profile", profile, "event", string(prettyJSON))
			}
		}
	}
}

// fireExpiredNotification logs the diagnostic reason, applies the
// per-session throttle, and fires the "Session Expired" notification.
func fireExpiredNotification(notifier notify.Notifier, cache *csmNotificationCache, profile string, session *sso.Session, reason string) {
	if session == nil {
		slog.Warn("No session data available for profile", "profile", profile, "reason", reason)
		return
	}

	slog.Info("CSM detected credentials problem",
		"profile", profile,
		"sessionID", session.SessionID,
		"reason", reason,
	)

	cacheKey := session.DisplayName
	if cacheKey == "" {
		cacheKey = session.SessionID
	}
	if !cache.ShouldNotify(cacheKey) {
		slog.Debug("Skipping CSM notification due to throttle window", "profile", profile, "key", cacheKey)
		return
	}

	notifier.Notify(notify.SessionExpired(session))
}

// checkSsoTokenCacheExists checks the on-disk SSO token cache for the
// session's profile; if missing, prompts the user to log in (throttled
// by the per-session cache window).
func (p *Provider) checkSsoTokenCacheExists(profile string, session *sso.Session, notifier notify.Notifier, cache *csmNotificationCache) {
	if !p.SsoTokenCacheExists(session) {
		slog.Info("SSO token cache does not exist, prompting for login", "profile", profile, "sessionID", session.SessionID)

		cacheKey := session.DisplayName
		if cacheKey == "" {
			cacheKey = session.SessionID
		}
		if !cache.ShouldNotify(cacheKey) {
			slog.Debug("Skipping cache check notification due to throttle window", "profile", profile, "key", cacheKey)
			return
		}

		notifier.Notify(notify.SessionExpired(session))
	} else {
		slog.Debug("SSO token cache exists", "profile", profile, "sessionID", session.SessionID)
	}
}

// getStringFromMap safely extracts a string value from a map
func getStringFromMap(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// getNumberFromMap safely extracts a numeric value (as float64, since
// encoding/json decodes JSON numbers into float64 by default) from a
// map of decoded JSON.
func getNumberFromMap(m map[string]interface{}, key string) (float64, bool) {
	if val, ok := m[key]; ok {
		if n, ok := val.(float64); ok {
			return n, true
		}
	}
	return 0, false
}

// SSO-ZEN marker comments. injectCsmConfigForProfiles writes these on
// the csm_enabled / csm_port keys; the read-side helpers below check
// for them to tell apart "our injected keys" from "the user's own CSM
// configuration".
const (
	csmEnabledComment = "SSO-ZEN: CSM monitoring enabled"
	csmPortComment    = "SSO-ZEN: CSM monitoring port"
)

// isSsoZenComment reports whether an ini key comment is one of ours.
// ini round-trips comments with a leading ';' and incidental whitespace,
// so trim defensively before comparing.
func isSsoZenComment(comment string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comment), ";"))
	return trimmed == csmEnabledComment || trimmed == csmPortComment
}

// hasSsoZenCsmConfig reports whether a section carries any key tagged
// with our SSO-ZEN marker.
func hasSsoZenCsmConfig(section *ini.Section) bool {
	for _, key := range section.Keys() {
		if isSsoZenComment(key.Comment) {
			return true
		}
	}
	return false
}

// injectCsmConfigForProfiles injects CSM configuration into each profile section
func injectCsmConfigForProfiles(ports map[string]int) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(homeDir, ".aws", "config")

	cfg, err := ini.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config file: %w", err)
	}

	for profile, port := range ports {
		var section *ini.Section
		if cfg.HasSection("profile " + profile) {
			section = cfg.Section("profile " + profile)
		} else if cfg.HasSection(profile) {
			section = cfg.Section(profile)
		} else {
			slog.Warn("Profile section not found", "profile", profile)
			continue
		}

		// Three cases:
		//   1. csm_enabled is missing → add fresh keys with SSO-ZEN comments.
		//   2. csm_enabled is ours (SSO-ZEN comment) → update the port
		//      (it can change across restarts). NewKey would error on the
		//      existing key, so use Key(...).SetValue.
		//   3. csm_enabled belongs to the user → leave it alone.
		switch {
		case !section.HasKey("csm_enabled"):
			if _, err := section.NewKey("csm_enabled", "true"); err != nil {
				slog.Error("Failed to add csm_enabled key", "profile", profile, "error", err)
				continue
			}
			section.Key("csm_enabled").Comment = csmEnabledComment
			if _, err := section.NewKey("csm_port", strconv.Itoa(port)); err != nil {
				slog.Error("Failed to add csm_port key", "profile", profile, "error", err)
				continue
			}
			section.Key("csm_port").Comment = csmPortComment
			slog.Debug("Added CSM config to profile", "profile", profile, "port", port)
		case hasSsoZenCsmConfig(section):
			section.Key("csm_enabled").SetValue("true")
			section.Key("csm_port").SetValue(strconv.Itoa(port))
			slog.Debug("Refreshed SSO-ZEN CSM config for profile", "profile", profile, "port", port)
		default:
			slog.Debug("CSM config already present and not ours; leaving alone", "profile", profile)
		}
	}

	if err := cfg.SaveTo(configPath); err != nil {
		return fmt.Errorf("failed to save config file: %w", err)
	}

	slog.Info("CSM config injected successfully", "profiles", len(ports))
	return nil
}

// BackupAwsConfig creates a timestamped backup of ~/.aws/config in the
// same directory. Called the first time the user enables CSM so they
// have a known-good copy before we start editing.
func BackupAwsConfig() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".aws", "config")

	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to stat config file: %w", err)
	}

	dir := filepath.Dir(configPath)
	timestamp := time.Now().Format("20060102-150405")
	backupName := fmt.Sprintf("config.ssozen.%s.bak", timestamp)
	backupPath := filepath.Join(dir, backupName)

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file for backup: %w", err)
	}

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	slog.Info("Created AWS config backup", "backup", backupPath)
	return nil
}

// RemoveCsmConfig removes the CSM configuration from the AWS config file.
// Called from MonitorLive's deferred cleanup and from the AWS provider
// when the user disables CSM.
func RemoveCsmConfig() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(homeDir, ".aws", "config")

	cfg, err := ini.Load(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to load config file: %w", err)
	}

	removedCount := 0

	for _, section := range cfg.Sections() {
		if strings.HasPrefix(section.Name(), "sso-session ") {
			continue
		}

		keysToRemove := []string{}
		for _, key := range section.Keys() {
			if isSsoZenComment(key.Comment) {
				keysToRemove = append(keysToRemove, key.Name())
			}
		}

		for _, keyName := range keysToRemove {
			section.DeleteKey(keyName)
			removedCount++
		}
	}

	if removedCount > 0 {
		if err := cfg.SaveTo(configPath); err != nil {
			return fmt.Errorf("failed to save config file: %w", err)
		}
		slog.Info("CSM config removed successfully", "keys_removed", removedCount)
	} else {
		slog.Debug("No SSO-ZEN CSM config found to remove")
	}

	return nil
}
