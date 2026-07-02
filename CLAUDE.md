# CLAUDE.md

Working notes for Claude on the SSO Zen codebase.

## Project

Cross-platform Go + Fyne tray app that monitors local SSO sessions (AWS
SSO today; Okta / Google / Azure AD next via the provider model). The
core is feature-complete and the project is being prepped to go public;
new work is net-new features, polish, and additional providers.

## How to work

- One change per PR is the default. Bundle trivially related changes if
  it makes the diff easier to read; mention in chat that you're bundling.
  The user often asks for larger themed bundles — follow the cadence the
  user sets.
- Open a PR for each change.
- If a task turns out to be wrong (the bug doesn't reproduce, or the
  refactor is much bigger than billed), stop and ask rather than plowing
  ahead.

## Commits

- Conventional-style commit subjects are fine but not required. Keep them short (`fix: …`, `refactor: …`, `ci: …`). The "why" goes in the body.
- Don't write GitHub-rendered Markdown with hard line wraps in PR bodies. The user finds it ugly; let GitHub flow each bullet/paragraph naturally.

## Code style

This is a Go project that should *feel* like a Go project. Match the
patterns of the file you're editing, and if something reads as
ceremonious, mention it. Specifics:

- **No `Get` prefix** on getters. `Settings()`, not `GetSettings()`.
- **No `XxxService` suffix** when the package name already says it. A notifier in package `notify` is `notify.Notifier`, not `notify.NotificationService`.
- **No stutter.** `aws.Provider`, not `aws.AwsSsoProvider`. `sso.Session` is fine; `session.Session` would be stuttery — but no need to invent new packages just for that.
- **Interfaces live with the consumer.** "Accept interfaces, return structs." The provider package exports a concrete `*Provider`; `monitor`, `tray`, and `ui` each declare the tiny interface they actually use.
- **Small interfaces.** Prefer one method when one method does it. `notify.Notifier { Notify(Notification) }` is the model.
- **Lifecycle via `context.Context`.** Prefer a blocking `Run(ctx) error` shape over `Start()/Stop()` ceremony. Use `Stop` only for cleanup that ctx can't express (lockfile removal, registry cleanup, etc.).
- **No `Set*Callback` wiring.** Pass deps at construction, or expose a channel / typed event from the producer (e.g. `monitor.Updates()`, `settings.Changes()`, `notify.LoginRequests()`).
- **`errors.Is` / `errors.As`** for sentinels, not `==`.
- **`strconv`** for number formatting, not `fmt.Sprintf("%d", …)`.
- **Don't `_` ignore errors from `os.UserHomeDir`** etc. — resolve once at startup, stash on the struct, surface the error.
- **No `// FooBar does X`** doc comments above `func FooBar()` that just restate the name. Comments should explain *why*, not *what*.
- **`gofmt` clean.** No tab/space mixing. CI gates this (`.github/workflows/build.yml`).
- **`go vet` and `staticcheck` clean.** Also CI-gated.
- **No emojis** in source or commit messages unless the user asks.

## Architecture

- `main.go` — orchestration only: construct providers, register them, wire channels between monitor / tray / notify / settings, run the LiveMonitor goroutines, run Fyne. No provider-specific knowledge.
- `sso/` — small shared types: `Session`, `Status`, the `Provider` interface, `CacheChecker`. No interfaces that get pre-declared "just in case" — consumers own those.
- `providers/providers.go` — global registry. `Register(p sso.Provider)` / `All() []sso.Provider`. `main` is the only thing that registers; everything else reads via `All()`.
- `providers/aws/` — self-contained AWS SSO provider. Owns its config parser, cache hash helpers, login flow (via the injected `CommandRunner`), CSM live monitor, AWS-scoped settings + settings UI, first-run defaults, and its session-row icon.
- `monitor/` — provider-agnostic session monitor. Walks `[]sso.Provider`, aggregates `ActiveSessions`, watches paths reported by any `Watchable` provider, runs per-session expiry timers, optionally probes via `Prober`. Emits the current set on `Updates()`.
- `notify/` — notification dispatcher. Picks Windows toast vs. Fyne fallback at construction. Exposes `LoginRequests()` so the host can fulfil Login-button clicks.
- `settings/` — global settings store (`settings.Settings` + provider-scoped `Providers map[string]json.RawMessage`). Helpers: `ProviderSettings(id, &out)`, `UpdateProviderSettings(id, v)`. Emits on `Changes()`.
- `tray/` — Fyne system tray + main-window UI. Reads `providers.All()` for the per-session icons (`IconProvider`). No AWS-specific code.
- `ui/` — non-tray Fyne bits: settings panel (iterates `ProviderSettingsUI` providers), console window for `RunOptions`, log window, fatal-error dialog.
- `services/` — cross-cutting glue that doesn't fit elsewhere: single-instance lock, autostart, crash log.

The optional provider capabilities (any of these is a type-assertion away from being picked up):

- `monitor.Watchable` — `PathsToWatch() []string`
- `monitor.Prober` — `Probe(ctx, *sso.Session) error`
- `LiveMonitor` (declared in `main.go`) — `MonitorLive(ctx, notify.Notifier) error`
- `tray.IconProvider` — `Icon() fyne.Resource` (icon shown beside that provider's session rows, not the system-tray icon)
- `ui.ProviderSettingsUI` — `SettingsUI(infoFactory) fyne.CanvasObject`

The AWS provider is the worked example — it implements every one of them.

## Things to remember

- `aws-sdk-go` v1 is in `go.mod` as `// indirect` (transitive dep). Don't add it back as a direct dep without a reason; login shells out to the `aws` CLI on purpose.
- `services/single_instance_lock_unix.go` removes the lockfile on `Unlock`. There's a known but low-priority race; leave that alone unless you understand it.
- Provider-scoped settings persist under `settings.Settings.Providers[id]` as `json.RawMessage`. Adding a new provider that wants settings → use `Store.ProviderSettings` / `Store.UpdateProviderSettings`; don't extend the top-level `Settings` struct.
- First-run defaults for a provider live in the provider (`aws.ApplyFirstRunDefaults` is the template). `main`'s first-run block is intentionally provider-agnostic.
- The CSM lifecycle is delicate: `RemoveCsmConfig` in the deferred cleanup runs *after* the new `MonitorLive` may have started injecting fresh `csm_port` keys. `main`'s `startLiveMonitor` serialises bounces under a mutex and waits on the previous goroutine's `done` before starting the next — keep that ordering if you touch it.
- The CSM event protocol changed mid-2026: a credential-resolution failure now arrives as `{Type: ApiCall, AttemptCount: 0}` with no exception fields. The filter in `providers/aws/csm.go` matches that shape first, then falls back to the older `SSO OIDC CreateToken InvalidGrantException` / `SSO Token does not exist` signals.

## When in doubt

- Match the patterns of the file you're editing, then improve them. Don't rewrite the world inside a small task.
- The user's bar is "feels like Go." If something feels ceremonious, mention it — it's probably worth a follow-up even if it's not the task at hand.
