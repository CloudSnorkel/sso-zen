# Contributing

SSO Zen is a small Go + Fyne tray app. The sections below cover the bits worth knowing before you open a PR.

## Setup

```bash
git clone https://github.com/CloudSnorkel/sso-zen.git
cd sso-zen
go mod download
go build ./...
go test ./...
```

CGO is required (Fyne pulls in GL). On Linux you'll need `libgl1-mesa-dev xorg-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev libglfw3-dev` (see `.github/workflows/build-platform.yml` for the canonical list).

CI gates on `gofmt -l .` being empty, `go vet ./...`, `staticcheck`, and `go test ./...`. Run them locally before pushing.

## Packaging

Packaged builds use `fyne package -os <platform>` on the native runner for each platform (linux/darwin/windows). The recipe is `.github/workflows/build-platform.yml`.

## Cutting a release

Tag-driven. Bump `v` and push the tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

That fires `.github/workflows/release.yml`, which invokes build-platform.yml with `release: true` on all three platforms and attaches the resulting artefacts (`.exe.zip`, `.tar.xz`, `.app.zip`) to the auto-generated GitHub Release.

## Adding a new provider

Providers live under `providers/<name>/`. The minimum interface a provider must implement is `sso.Provider`:

```go
type Provider interface {
    ID() string
    Name() string
    ActiveSessions(ctx context.Context) ([]*Session, error)
    Login(ctx context.Context, session *Session) error
}
```

Walkthrough:

1. **Create the package.** `providers/<name>/<name>_provider.go` with a `Provider` struct and a `NewProvider(...)` constructor that takes whatever deps it needs (typically the settings store, a `CommandRunner`-shaped thing for any subprocess flows).

2. **Register it from main.** After construction:

   ```go
   providers.Register(myProvider)
   ```

   Monitor, tray, and the settings panel walk `providers.All()`. They don't import provider packages directly.

3. **Implement optional capabilities** by adding methods that match the capability interface. Consumers type-assert against these, so you only pay for what you implement:

   - `monitor.Watchable { PathsToWatch() []string }` - paths the file watcher should monitor (e.g. config files). Triggers a debounced refresh on change.

   - `monitor.Prober { Probe(ctx, *sso.Session) error }` - opt-in active probe near expiry. Avoids false-positive "Expiring Soon" notifications when an SDK can silently refresh creds.

   - `LiveMonitor { MonitorLive(ctx, notify.Notifier) error }` - a long-lived monitoring loop (e.g. AWS CSM). Main runs one goroutine per LiveMonitor provider and bounces on session-set or settings changes.

   - `tray.IconProvider { Icon() fyne.Resource }` - custom icon shown beside that provider's session rows. Falls back to `theme.AccountIcon()` if not implemented.

   - `ui.ProviderSettingsUI { SettingsUI(infoFactory) fyne.CanvasObject }` - a settings card rendered in the global Settings panel between General and Notifications.

4. **Provider-scoped settings.** Define a struct, persist it via `settings.Store.UpdateProviderSettings(id, ...)`, load via `Store.ProviderSettings(id, &out)`. The AWS provider is the canonical example (`providers/aws/settings.go`).

5. **First-run defaults** go in your package, not in main. Expose something like `ApplyFirstRunDefaults(p *Provider)` and have main call it under `settings.IsFirstRun()`.

The AWS provider (`providers/aws/`) is the best example. It implements every optional capability above.

## PRs

- One change per PR. Bundle trivially related items if it makes the diff easier to read.
- Conventional-style commit subjects are fine but not required. Keep them short; "why" goes in the body.
- CI must pass. The format + build matrix runs on every PR.
