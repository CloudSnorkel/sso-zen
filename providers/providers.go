// Package providers is the shared registry of SSO providers. Each
// provider package (e.g. providers/aws) imports this and calls Register
// from main once it's been constructed. Consumers (main, monitor, tray,
// settings UI) read the live list via All.
package providers

import "sso-zen/sso"

var registry []sso.Provider

// Register adds p to the global provider registry. Call once per
// provider at startup, after construction.
func Register(p sso.Provider) {
	registry = append(registry, p)
}

// All returns the currently registered providers, in registration order.
func All() []sso.Provider {
	out := make([]sso.Provider, len(registry))
	copy(out, registry)
	return out
}
