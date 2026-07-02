package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"sso-zen/notify"
	"sso-zen/settings"
	"sso-zen/sso"

	"github.com/fsnotify/fsnotify"
)

// Notifier is the subset of notification behaviour the monitor needs.
// Interface declared here per "interfaces live with the consumer";
// notify.Service satisfies it.
type Notifier interface {
	Notify(notify.Notification)
}

// Prober is implemented by providers that can actively probe a session's
// credentials. Today only the AWS provider implements it (via
// `aws sts get-caller-identity`).
type Prober interface {
	Probe(ctx context.Context, session *sso.Session) error
}

// Watchable is implemented by providers that have on-disk state we want
// to react to immediately (e.g. AWS's ~/.aws/config). The monitor walks
// the provider list at startup and adds these paths to its fsnotify
// watcher; the existing debounced refresh-on-change logic then picks up
// edits without waiting for the next expiry timer.
type Watchable interface {
	PathsToWatch() []string
}

// Monitor monitors SSO sessions and triggers notifications
type Monitor struct {
	providers       []sso.Provider
	notifier        Notifier
	settingsService *settings.Store

	currentSessions map[string]*sso.Session // sessionID -> session
	sessionsMutex   sync.RWMutex

	// UI updates. updates carries the current session set whenever it
	// changes; consumers (tray, the CSM-restart diff in main) read it
	// to react. Buffer of one with non-blocking send so a slow consumer
	// can never block the monitor.
	updates     chan []*sso.Session
	updateTimer *time.Timer
	updateMutex sync.Mutex

	// File monitoring
	watcher *fsnotify.Watcher

	// ctx is the running context. Set by Run; nil before Run starts.
	// File watcher and probe goroutines read it to know when to exit.
	ctx context.Context
	wg  sync.WaitGroup

	// Notification bookkeeping
	expiringNotified map[string]bool // sessionID -> whether expiring-soon was handled in current window

	// expiredHandledFor tracks the expiresAt for which we last fired the
	// "Expired" notification (or started a probe for it). Prevents firing
	// again on subsequent refreshes that report the same expired state.
	// Cleared implicitly when ExpiresAt changes.
	expiredHandledFor map[string]time.Time

	// probeInFlight tracks sessions with an active sts:GetCallerIdentity
	// probe so we don't fire a second probe (or notification) while one
	// is already in motion.
	probeInFlight map[string]bool

	// Per-session expiry timers. The cache's expiresAt is not visited by
	// any file event, so without these the "Expiring Soon" → "Expired"
	// transitions never fire for an idle session.
	expiryTimers      map[string]*expiryTimerSet // sessionID -> timers
	expiryTimersMutex sync.Mutex
}

type expiryTimerSet struct {
	threshold *time.Timer
	expiry    *time.Timer
}

func (ts *expiryTimerSet) stop() {
	if ts.threshold != nil {
		ts.threshold.Stop()
	}
	if ts.expiry != nil {
		ts.expiry.Stop()
	}
}

// New creates a new session monitor over the given providers. The
// monitor aggregates ActiveSessions across all providers on each refresh
// and emits the combined set on Updates.
func New(providers []sso.Provider, notifier Notifier, settingsService *settings.Store) *Monitor {
	return &Monitor{
		providers:         providers,
		notifier:          notifier,
		settingsService:   settingsService,
		currentSessions:   make(map[string]*sso.Session),
		expiringNotified:  make(map[string]bool),
		expiredHandledFor: make(map[string]time.Time),
		probeInFlight:     make(map[string]bool),
		expiryTimers:      make(map[string]*expiryTimerSet),
		updates:           make(chan []*sso.Session, 1),
	}
}

// Updates returns a channel that emits the current session set whenever
// it changes. Buffered (1) with non-blocking send: slow consumers see
// only the latest snapshot, never block the monitor.
func (sms *Monitor) Updates() <-chan []*sso.Session {
	return sms.updates
}

// Run drives the session monitor. Blocks until ctx is cancelled,
// returning ctx.Err(). Sets up the file watcher, runs an initial
// refresh, drives the file-watch loop, and cleans up timers and the
// watcher on exit.
func (sms *Monitor) Run(ctx context.Context) error {
	slog.Info("Starting session monitor")
	sms.ctx = ctx

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	sms.watcher = watcher

	if err := sms.setupFileMonitoring(); err != nil {
		watcher.Close()
		return err
	}

	sms.wg.Add(1)
	go sms.fileMonitorLoop()

	if err := sms.Refresh(ctx); err != nil {
		slog.Error("Failed to perform initial session refresh", "error", err)
		// Initial refresh failure isn't fatal — keep running so a
		// later file event can recover.
	}

	<-ctx.Done()
	slog.Info("Stopping session monitor")

	sms.wg.Wait()
	watcher.Close()

	sms.updateMutex.Lock()
	if sms.updateTimer != nil {
		sms.updateTimer.Stop()
	}
	sms.updateMutex.Unlock()

	sms.expiryTimersMutex.Lock()
	for sessionID, timers := range sms.expiryTimers {
		timers.stop()
		delete(sms.expiryTimers, sessionID)
	}
	sms.expiryTimersMutex.Unlock()

	slog.Info("Session monitor stopped")
	return ctx.Err()
}

// Sessions returns all current sessions
func (sms *Monitor) Sessions() []*sso.Session {
	sms.sessionsMutex.RLock()
	defer sms.sessionsMutex.RUnlock()

	sessions := make([]*sso.Session, 0, len(sms.currentSessions))
	for _, session := range sms.currentSessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// Refresh refreshes sessions from all providers
func (sms *Monitor) Refresh(ctx context.Context) error {
	slog.Debug("Refreshing sessions from providers", "count", len(sms.providers))

	var all []*sso.Session
	for _, p := range sms.providers {
		sessions, err := p.ActiveSessions(ctx)
		if err != nil {
			slog.Warn("Failed to get sessions from provider", "provider", p.ID(), "error", err)
			continue
		}
		all = append(all, sessions...)
	}

	slog.Debug("Providers returned sessions", "count", len(all))

	return sms.updateSessions(all)
}

// setupFileMonitoring registers each Watchable provider's paths with
// fsnotify. Missing paths are silently skipped — the provider may not
// yet have any state on disk.
func (sms *Monitor) setupFileMonitoring() error {
	for _, p := range sms.providers {
		w, ok := p.(Watchable)
		if !ok {
			continue
		}
		for _, path := range w.PathsToWatch() {
			if err := sms.watcher.Add(path); err != nil {
				slog.Debug("Could not watch path", "provider", p.ID(), "path", path, "error", err)
				continue
			}
			slog.Debug("Watching path", "provider", p.ID(), "path", path)
		}
	}
	return nil
}

// fileMonitorLoop runs the file monitoring loop
func (sms *Monitor) fileMonitorLoop() {
	defer sms.wg.Done()

	// Debounce timer to avoid too many refreshes
	var refreshTimer *time.Timer

	for {
		select {
		case <-sms.ctx.Done():
			if refreshTimer != nil {
				refreshTimer.Stop()
			}
			return

		case event, ok := <-sms.watcher.Events:
			if !ok {
				return
			}

			// Only refresh on write/create/remove events
			if event.Op&fsnotify.Write == fsnotify.Write ||
				event.Op&fsnotify.Create == fsnotify.Create ||
				event.Op&fsnotify.Remove == fsnotify.Remove {
				slog.Debug("File change detected", "file", event.Name, "op", event.Op)

				// Cancel any existing timer
				if refreshTimer != nil {
					refreshTimer.Stop()
				}

				// Debounce: wait 1 second before refreshing
				refreshTimer = time.AfterFunc(1*time.Second, func() {
					if err := sms.Refresh(sms.ctx); err != nil {
						slog.Error("Error refreshing sessions", "error", err)
					}
				})
			}

		case err, ok := <-sms.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("File watcher error", "error", err)
		}
	}
}

// updateSessions updates the current sessions and processes individual updates
func (sms *Monitor) updateSessions(newSessions []*sso.Session) error {
	sms.sessionsMutex.Lock()
	defer sms.sessionsMutex.Unlock()

	// Create a map of new sessions for easy lookup
	newSessionsMap := make(map[string]*sso.Session)
	for _, session := range newSessions {
		newSessionsMap[session.SessionID] = session
	}

	// Check for removed sessions
	for sessionID := range sms.currentSessions {
		if _, exists := newSessionsMap[sessionID]; !exists {
			// Session was removed
			delete(sms.currentSessions, sessionID)
		}
	}

	var current *settings.Settings
	if sms.settingsService != nil {
		current = sms.settingsService.Settings()
	}
	probeEnabled := current != nil && current.ProbeNearExpiry

	// Check for new and updated sessions
	for sessionID, newSession := range newSessionsMap {
		oldSession, exists := sms.currentSessions[sessionID]

		if !exists {
			// New session discovered - no notification needed
			slog.Info("Session discovered",
				"sessionID", newSession.SessionID,
				"displayName", newSession.DisplayName,
			)
		} else if oldSession.Status != newSession.Status &&
			(oldSession.Status == sso.StatusActive || oldSession.Status == sso.StatusExpiringSoon) &&
			newSession.Status == sso.StatusExpired {
			// Session looks expired. If the user opted in to probing, run
			// sts:GetCallerIdentity before notifying — the cache's
			// expiresAt may be stale because the SDK silently refreshes
			// via the refresh token on demand.
			if probeEnabled && sessionHasProber(newSession) && !sms.probeInFlight[sessionID] &&
				!sms.expiredHandledFor[sessionID].Equal(newSession.ExpiresAt) {
				sms.expiredHandledFor[sessionID] = newSession.ExpiresAt
				sms.probeInFlight[sessionID] = true
				slog.Info("Session looks expired; probing before notifying",
					"sessionID", newSession.SessionID,
					"displayName", newSession.DisplayName,
				)
				go sms.runProbe(newSession, probeModeExpired, 0)
			} else if !probeEnabled && !sms.expiredHandledFor[sessionID].Equal(newSession.ExpiresAt) {
				sms.expiredHandledFor[sessionID] = newSession.ExpiresAt
				slog.Info("Session expired",
					"sessionID", newSession.SessionID,
					"displayName", newSession.DisplayName,
				)
				if current != nil && current.NotifyWhenExpired {
					go sms.notifier.Notify(notify.SessionExpired(newSession))
				}
			}
		}

		// Determine and handle expiring-soon state based on settings
		if current != nil && current.NotifyWhenExpiring && newSession.Status != sso.StatusExpired {
			timeUntilExpiry := newSession.TimeUntilExpiry()
			if timeUntilExpiry > 0 && timeUntilExpiry <= current.ExpiryNotificationThreshold {
				// In the threshold window.
				if probeEnabled && sessionHasProber(newSession) {
					if !sms.expiringNotified[sessionID] && !sms.probeInFlight[sessionID] {
						sms.probeInFlight[sessionID] = true
						slog.Info("Session near expiry; probing instead of notifying",
							"sessionID", newSession.SessionID,
							"in", timeUntilExpiry,
						)
						go sms.runProbe(newSession, probeModeExpiringSoon, timeUntilExpiry)
					}
				} else {
					if newSession.Status == sso.StatusActive {
						newSession.Status = sso.StatusExpiringSoon
					}
					if !sms.expiringNotified[sessionID] {
						slog.Info("Session expiring soon",
							"sessionID", newSession.SessionID,
							"displayName", newSession.DisplayName,
							"in", timeUntilExpiry,
						)
						go sms.notifier.Notify(notify.SessionExpiringSoon(newSession, timeUntilExpiry))
						sms.expiringNotified[sessionID] = true
					}
				}
			} else {
				// Outside the window; reset so we can act next time around.
				delete(sms.expiringNotified, sessionID)
			}
		}

		// Update the session in our map
		sms.currentSessions[sessionID] = newSession
	}

	// Drop tracking state for sessions that disappeared.
	for sessionID := range sms.expiredHandledFor {
		if _, exists := sms.currentSessions[sessionID]; !exists {
			delete(sms.expiredHandledFor, sessionID)
		}
	}

	// Reschedule per-session expiry timers based on the new session set.
	// The threshold drives when we wake up to fire "Expiring Soon"; the
	// expiry tick catches the transition to "Expired" for idle sessions
	// the file watcher would otherwise miss.
	var threshold time.Duration
	if sms.settingsService != nil {
		threshold = sms.settingsService.Settings().ExpiryNotificationThreshold
	}
	sms.rescheduleExpiryTimers(threshold)

	// Schedule UI update with debouncing
	sms.scheduleUIUpdate()

	return nil
}

// rescheduleExpiryTimers cancels and re-creates timers for the current
// session set. Caller must hold sessionsMutex.
func (sms *Monitor) rescheduleExpiryTimers(threshold time.Duration) {
	sms.expiryTimersMutex.Lock()
	defer sms.expiryTimersMutex.Unlock()

	// Drop timers for sessions that no longer exist.
	for sessionID, timers := range sms.expiryTimers {
		if _, exists := sms.currentSessions[sessionID]; !exists {
			timers.stop()
			delete(sms.expiryTimers, sessionID)
		}
	}

	now := time.Now()
	for sessionID, session := range sms.currentSessions {
		if old, exists := sms.expiryTimers[sessionID]; exists {
			old.stop()
		}

		set := &expiryTimerSet{}
		thresholdDelay := time.Until(session.ExpiresAt) - threshold
		if thresholdDelay > 0 {
			set.threshold = time.AfterFunc(thresholdDelay, sms.onExpiryTimerFired)
		}
		expiryDelay := session.ExpiresAt.Sub(now)
		if expiryDelay > 0 {
			set.expiry = time.AfterFunc(expiryDelay, sms.onExpiryTimerFired)
		}
		sms.expiryTimers[sessionID] = set
		slog.Info("Scheduled expiry timers",
			"sessionID", sessionID,
			"thresholdIn", thresholdDelay,
			"expiryIn", expiryDelay,
			"threshold", threshold,
		)
	}
}

// onExpiryTimerFired re-runs a full refresh. Refresh re-reads the cache
// (which may have been silently updated by an SDK-side refresh-token
// swap) and updateSessions handles the status transition and reschedule.
func (sms *Monitor) onExpiryTimerFired() {
	if sms.ctx == nil || sms.ctx.Err() != nil {
		return
	}
	slog.Info("Expiry timer fired; refreshing sessions")
	if err := sms.Refresh(sms.ctx); err != nil {
		slog.Error("Expiry timer refresh failed", "error", err)
	}
}

type probeMode int

const (
	probeModeExpiringSoon probeMode = iota
	probeModeExpired
)

const probeTimeout = 30 * time.Second

// sessionHasProber reports whether the session's provider can probe.
func sessionHasProber(s *sso.Session) bool {
	if s == nil || s.Provider == nil {
		return false
	}
	_, ok := s.Provider.(Prober)
	return ok
}

// runProbe runs sts:GetCallerIdentity for the given session. Success
// means current credentials work — possibly after a silent SDK refresh
// using the refresh token; we trigger a Refresh to re-read the cache and
// reschedule timers against any new expiresAt. Failure means we should
// notify the user; the notification matches what would have fired
// without the probe (Expiring Soon vs. Expired).
func (sms *Monitor) runProbe(session *sso.Session, mode probeMode, timeUntilExpiry time.Duration) {
	defer func() {
		sms.sessionsMutex.Lock()
		delete(sms.probeInFlight, session.SessionID)
		sms.sessionsMutex.Unlock()
	}()

	prober, ok := session.Provider.(Prober)
	if !ok {
		return
	}

	probeCtx, cancel := context.WithTimeout(sms.ctx, probeTimeout)
	defer cancel()

	err := prober.Probe(probeCtx, session)
	if err != nil {
		slog.Info("Probe failed; firing notification",
			"sessionID", session.SessionID,
			"mode", mode,
			"error", err,
		)
		var current *settings.Settings
		if sms.settingsService != nil {
			current = sms.settingsService.Settings()
		}
		switch mode {
		case probeModeExpired:
			if current != nil && current.NotifyWhenExpired {
				sms.notifier.Notify(notify.SessionExpired(session))
			}
		case probeModeExpiringSoon:
			sms.sessionsMutex.Lock()
			sms.expiringNotified[session.SessionID] = true
			sms.sessionsMutex.Unlock()
			if current != nil && current.NotifyWhenExpiring {
				sms.notifier.Notify(notify.SessionExpiringSoon(session, timeUntilExpiry))
			}
		}
		return
	}

	slog.Info("Probe succeeded; refreshing to pick up any silent SDK refresh",
		"sessionID", session.SessionID,
		"mode", mode,
	)
	// Suppress re-probe of the same window if Refresh doesn't move the
	// expiresAt. For expiring-soon this means setting expiringNotified;
	// for expired it's already set via expiredHandledFor.
	if mode == probeModeExpiringSoon {
		sms.sessionsMutex.Lock()
		sms.expiringNotified[session.SessionID] = true
		sms.sessionsMutex.Unlock()
	}
	if err := sms.Refresh(sms.ctx); err != nil {
		slog.Error("Failed to refresh after successful probe", "error", err)
	}
}

// scheduleUIUpdate debounces UI updates and sends the latest session
// set on Updates(). Non-blocking — drops if a previous update is still
// buffered (the consumer will catch up to the newest state on next read).
func (sms *Monitor) scheduleUIUpdate() {
	sms.updateMutex.Lock()
	defer sms.updateMutex.Unlock()

	if sms.updateTimer != nil {
		sms.updateTimer.Stop()
	}

	sms.updateTimer = time.AfterFunc(500*time.Millisecond, func() {
		sessions := sms.Sessions()
		select {
		case sms.updates <- sessions:
		default:
			// Consumer hasn't drained the previous frame; they'll see
			// the latest state on next read.
		}
	})
}
