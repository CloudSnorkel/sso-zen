package aws

import (
	"log/slog"

	"sso-zen/settings"
)

// AWSSettings holds AWS-provider-scoped settings, persisted as a JSON
// blob under settings.Settings.Providers["aws-sso"].
type AWSSettings struct {
	// EnableCSM enables AWS Client-Side Monitoring (the LiveMonitor
	// path). Off by default — turning it on edits ~/.aws/config to
	// inject csm_port keys per profile, which not all users want.
	EnableCSM bool
}

// settingsProviderID is the key under settings.Providers where this
// provider stores its JSON blob.
const settingsProviderID = "aws-sso"

// Settings reads the AWS provider's settings from the global store,
// applying defaults for missing fields. The store is the source of
// truth — there is no in-memory cache.
func (p *Provider) Settings() AWSSettings {
	var s AWSSettings
	if _, err := p.settingsStore.ProviderSettings(settingsProviderID, &s); err != nil {
		slog.Warn("Failed to load AWS provider settings; using defaults", "error", err)
	}
	return s
}

// UpdateSettings runs update against the current settings and persists
// the result. Triggers a Changes() emission on the global store.
func (p *Provider) UpdateSettings(update func(*AWSSettings)) error {
	s := p.Settings()
	prev := s
	update(&s)
	// First-time enable: snapshot ~/.aws/config so users have a known-
	// good copy before MonitorLive edits it.
	if s.EnableCSM && !prev.EnableCSM {
		if err := BackupAwsConfig(); err != nil {
			slog.Warn("Failed to back up AWS config on enable CSM", "error", err)
		}
	}
	// Disable: pull our injected keys out immediately so the user
	// doesn't see them lingering in ~/.aws/config until restart.
	if !s.EnableCSM && prev.EnableCSM {
		if err := RemoveCsmConfig(); err != nil {
			slog.Warn("Failed to remove CSM config on disable", "error", err)
		}
	}
	return p.settingsStore.UpdateProviderSettings(settingsProviderID, s)
}

// SetSettingsStore wires the global settings store into the provider.
// Called once from main after construction.
func (p *Provider) SetSettingsStore(store *settings.Store) {
	p.settingsStore = store
}

// ApplyFirstRunDefaults persists this provider's first-run defaults so
// they're present in the saved settings file from the very first save.
// Called from main on a fresh install. Per-provider defaults live here
// rather than as a hard-coded block in main (E8).
func ApplyFirstRunDefaults(p *Provider) {
	defaults := AWSSettings{
		EnableCSM: false,
	}
	if err := p.settingsStore.UpdateProviderSettings(settingsProviderID, defaults); err != nil {
		slog.Warn("Failed to apply AWS first-run defaults", "error", err)
	}
}
