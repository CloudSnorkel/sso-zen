package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"sso-zen/monitor"
	"sso-zen/notify"
	"sso-zen/providers"
	"sso-zen/providers/aws"
	"sso-zen/services"
	"sso-zen/settings"
	"sso-zen/sso"
	"sso-zen/tray"
	"sso-zen/ui"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	_ "embed"
)

//go:embed assets/64x64.png
var icon []byte

// LiveMonitor is implemented by providers that can run a long-lived
// monitoring loop alongside the file-watch + timer based monitor (e.g.
// AWS CSM). Main starts a goroutine per LiveMonitor provider and
// restarts it whenever the global session set changes — the provider
// re-snapshots its own state on each start.
type LiveMonitor interface {
	MonitorLive(ctx context.Context, notifier notify.Notifier) error
}

// isDebugBuild returns true if the application is running in debug mode
func isDebugBuild() bool {
	return os.Getenv("SSO_ZEN_DEBUG") == "1"
}

func main() {
	backgroundFlag := flag.Bool("background", false, "start without opening the main window (used by autostart)")
	flag.Parse()

	// LevelVar so the settings panel can change log verbosity at
	// runtime without recreating the slog handler.
	var levelVar slog.LevelVar
	levelVar.Set(slog.LevelInfo)
	if isDebugBuild() {
		levelVar.Set(slog.LevelDebug)
	}
	// Tee log output to an in-memory ring buffer so the UI Log window
	// has something to show on Windows autostart launches where stdout
	// is detached. The ring's size is reconfigured from settings once
	// they've loaded; before that it holds startup lines at the default.
	//
	// Buffer first, stdout second on purpose: io.MultiWriter
	// short-circuits on the first writer error. Release Windows builds
	// linked with -H windowsgui (which `fyne package -os windows`
	// does) have no attached console, so os.Stdout.Write returns "the
	// handle is invalid" and the ring never sees the bytes if it sits
	// after stdout in the chain. LogBuffer.Write never errors, so
	// putting it first guarantees the ring catches every record even
	// when stdout is dead.
	logBuffer := services.NewLogBuffer(1000)
	logWriter := io.MultiWriter(logBuffer, os.Stdout)
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: &levelVar})))
	slog.Info("Starting SSO Zen")

	singleInstanceLock := services.NewSingleInstanceLock()
	defer singleInstanceLock.Unlock()
	singleInstanceLock.TryLockAndExit()

	fyneApp := app.NewWithID("com.cloudsnorkel.ssozen")
	fyneApp.SetIcon(fyne.NewStaticResource("icon", icon))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notificationService := notify.NewService(fyneApp)
	settingsService := settings.New(fyneApp)
	logBuffer.Resize(settingsService.Settings().LogBufferLines)
	// SSO_ZEN_DEBUG=1 wins over the persisted setting — it's a
	// developer-only override for cranking up logs without poking
	// settings.json.
	if !isDebugBuild() {
		levelVar.Set(parseLogLevel(settingsService.Settings().LogLevel))
	}
	logWindow := ui.NewLogWindow(fyneApp, logBuffer)

	// Provider construction + registration. Each provider's first-run
	// defaults live with the provider (E8); main no longer hard-codes
	// "disable CSM on first run" here.
	awsProvider, err := aws.NewProvider(uiCommandRunner{})
	if err != nil {
		fatalStartupError(fyneApp, "Could not initialise the AWS SSO provider.", err)
	}
	awsProvider.SetSettingsStore(settingsService)
	if settingsService.IsFirstRun() {
		aws.ApplyFirstRunDefaults(awsProvider)
	}
	providers.Register(awsProvider)

	registered := providers.All()

	sessionMonitoring := monitor.New(registered, notificationService, settingsService)
	trayService := tray.New(ctx, fyneApp, sessionMonitoring, notificationService, logWindow)
	startupService := services.NewStartupService()

	// Drain notify's LoginRequests in a goroutine — when the user clicks
	// Login on a toast, find the session and run its login flow, then
	// reply on Done so notify can show the follow-up toast.
	go func() {
		for req := range notificationService.LoginRequests() {
			err := loginForSessionID(ctx, sessionMonitoring, req.SessionID)
			req.Done <- err
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		slog.Info("Received shutdown signal")
		cancel()
		fyne.Do(func() { fyneApp.Quit() })
	}()

	if !*backgroundFlag {
		trayService.ShowWindow()
	}

	settingsPanel := ui.CreateSettingsPanel(settingsService, startupService, registered, logWindow, func() {
		trayService.ShowSessions()
	})
	trayService.SetSettingsContainer(settingsPanel)

	if settingsService.IsFirstRun() {
		slog.Info("First run detected - showing settings with autostart disabled")
		_ = settingsService.UpdateSettings(func(s *settings.Settings) {
			s.StartWithComputer = false
		})
		trayService.ShowWindow()
		trayService.ShowSettings()
	}

	// LiveMonitor orchestration. Per E4, the AWS CSM lifecycle lives in
	// the AWS provider; main just runs each LiveMonitor in a loop, kills
	// the active goroutine on session or settings changes, and restarts
	// it. The provider's MonitorLive re-snapshots state at the start of
	// each run.
	type liveRun struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	var liveMu sync.Mutex
	liveRuns := make(map[string]*liveRun)

	// startLiveMonitor stops the active goroutine for p (if any) and
	// starts a fresh one. Held under liveMu so concurrent bounces from
	// the Updates and Changes drains can't race into double-start.
	startLiveMonitor := func(p sso.Provider, lm LiveMonitor) {
		liveMu.Lock()
		defer liveMu.Unlock()

		if existing := liveRuns[p.ID()]; existing != nil {
			slog.Info("Stopping LiveMonitor", "provider", p.ID())
			existing.cancel()
			<-existing.done
			delete(liveRuns, p.ID())
		}

		// Skip start until there are actually sessions to monitor.
		// MonitorLive idles on empty sets anyway, but starting a real
		// goroutine for that is wasteful; let the Updates drain trigger
		// the real start when sessions land.
		if len(sessionMonitoring.Sessions()) == 0 {
			slog.Debug("Skipping LiveMonitor start: no sessions yet", "provider", p.ID())
			return
		}

		runCtx, runCancel := context.WithCancel(ctx)
		done := make(chan struct{})
		liveRuns[p.ID()] = &liveRun{cancel: runCancel, done: done}

		slog.Info("Starting LiveMonitor", "provider", p.ID())
		go func() {
			defer close(done)
			if err := lm.MonitorLive(runCtx, notificationService); err != nil && err != context.Canceled {
				slog.Error("LiveMonitor exited with error", "provider", p.ID(), "error", err)
			}
		}()
	}

	bounceLiveMonitors := func() {
		for _, p := range registered {
			if lm, ok := p.(LiveMonitor); ok {
				startLiveMonitor(p, lm)
			}
		}
	}

	// Drain monitor.Updates(): fan out to tray + bounce LiveMonitors
	// when the session set changes (so e.g. CSM picks up fresh profile
	// ports).
	go func() {
		var lastSessionIDs map[string]struct{}
		for sessions := range sessionMonitoring.Updates() {
			trayService.UpdateSessions(sessions)

			current := sessionIDSet(sessions)
			if !sessionIDSetsEqual(lastSessionIDs, current) {
				lastSessionIDs = current
				bounceLiveMonitors()
			}
		}
	}()

	// Drain settings.Changes(): resize the log ring, re-apply the log
	// level (unless the dev env override is on), bounce LiveMonitors
	// ONLY when a provider-scoped settings blob actually changed, and
	// trigger a session refresh.
	//
	// Bouncing LiveMonitors unconditionally on every change closed
	// CSM's UDP sockets every time the user tweaked a global setting
	// (log level, notification threshold) — the in-flight ReadFrom
	// then returned with net.ErrClosed and we'd log a spurious "Error
	// reading from UDP socket" for each profile. Compare the Providers
	// map to scope the bounce to changes that actually affect them.
	go func() {
		var prevProviders map[string]json.RawMessage
		for s := range settingsService.Changes() {
			logBuffer.Resize(s.LogBufferLines)
			if !isDebugBuild() {
				levelVar.Set(parseLogLevel(s.LogLevel))
			}
			if !providerSettingsEqual(prevProviders, s.Providers) {
				prevProviders = copyProviderSettings(s.Providers)
				bounceLiveMonitors()
			}
			if err := sessionMonitoring.Refresh(ctx); err != nil {
				slog.Error("Failed to refresh sessions on settings change", "error", err)
			}
		}
	}()

	go func() {
		<-trayService.QuitChannel()
		slog.Info("Quit requested from tray service")
		cancel()
		fyne.Do(func() { fyneApp.Quit() })
	}()

	monitorDone := make(chan error, 1)
	go func() { monitorDone <- sessionMonitoring.Run(ctx) }()

	slog.Info("SSO Zen started successfully")

	if panicked := runFyneApp(fyneApp); panicked {
		slog.Error("Fyne run panicked; relaunching in background")
		singleInstanceLock.Unlock()
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe, "--background")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if startErr := cmd.Start(); startErr != nil {
				slog.Error("Failed to relaunch", "error", startErr)
			} else {
				_ = cmd.Process.Release()
			}
		}
		os.Exit(1)
	}

	slog.Info("Shutting down SSO Zen")
	cancel()
	<-monitorDone
	slog.Info("SSO Zen stopped")
}

// uiCommandRunner adapts ui.RunOptions to aws.CommandRunner so the AWS
// provider doesn't depend on the UI package.
type uiCommandRunner struct{}

func (uiCommandRunner) RunOptions(ctx context.Context, title string, options []aws.Option) error {
	uiOptions := make([]ui.Option, len(options))
	for i, opt := range options {
		uiCmds := make([]ui.Command, len(opt))
		for j, c := range opt {
			uiCmds[j] = ui.Command{Name: c.Name, Args: c.Args, Env: c.Env, Banner: c.Banner}
		}
		uiOptions[i] = uiCmds
	}
	return ui.RunOptions(ctx, title, uiOptions)
}

// loginForSessionID finds the session by ID and runs its login flow,
// refreshing on success.
func loginForSessionID(ctx context.Context, m *monitor.Monitor, sessionID string) error {
	for _, session := range m.Sessions() {
		if session.SessionID == sessionID {
			if err := session.Login(ctx, session); err != nil {
				return err
			}
			return m.Refresh(ctx)
		}
	}
	return fmt.Errorf("session not found: %s", sessionID)
}

// sessionIDSet returns the set of session IDs in sessions. Used to
// detect when the session set has changed in a way that warrants
// bouncing LiveMonitors (which re-snapshot their state on each start).
func sessionIDSet(sessions []*sso.Session) map[string]struct{} {
	set := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		set[s.SessionID] = struct{}{}
	}
	return set
}

func sessionIDSetsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// fatalStartupError surfaces an error that prevented startup. It logs
// the underlying cause, opens a window with the same text via Fyne so
// the user isn't left staring at a tray that never appeared, blocks
// until the user dismisses it, then exits.
func fatalStartupError(fyneApp fyne.App, summary string, err error) {
	slog.Error(summary, "error", err)
	ui.ShowFatalError(fyneApp, fmt.Sprintf("%s\n\n%s\n\nSSO Zen will now exit.", summary, err))
	os.Exit(1)
}

// providerSettingsEqual reports whether two provider-scoped settings
// maps carry the same JSON payload for the same keys. Used to scope
// LiveMonitor bounces to changes that actually affect them (a global
// setting like log level shouldn't restart CSM).
func providerSettingsEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
}

// copyProviderSettings clones the map (and its byte slices) so a later
// in-place mutation by the settings store can't change the snapshot we
// compared against.
func copyProviderSettings(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		cp := make(json.RawMessage, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// parseLogLevel maps the persisted settings.LogLevel string to a slog
// level. Unknown / empty values fall back to INFO so an older
// settings.json without the field still gets sensible filtering.
func parseLogLevel(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// runFyneApp runs fyneApp.Run with a panic guard. Returns true if a panic was
// recovered. The recovered panic and its stack are written to the crash log.
func runFyneApp(fyneApp fyne.App) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			services.LogPanic("fyne.Run", r)
		}
	}()
	fyneApp.Run()
	return false
}
