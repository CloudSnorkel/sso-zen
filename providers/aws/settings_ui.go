package aws

import (
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// SettingsCardTitle is the heading shown in the settings panel for this
// provider's card.
const SettingsCardTitle = "AWS CSM"

// SettingsUI returns the settings card for this provider. infoFactory
// builds an info-button popup widget — the UI package owns the popup
// implementation, the provider just supplies the body text. Returning a
// fyne.CanvasObject (not the inner widgets) keeps the host generic; it
// just lays out whatever each provider returns.
func (p *Provider) SettingsUI(infoFactory func(title, markdown string) *widget.Button) fyne.CanvasObject {
	current := p.Settings()

	csmEnabledToggle := widget.NewCheck("Enable CSM (Client-Side Monitoring)", func(checked bool) {
		if err := p.UpdateSettings(func(s *AWSSettings) {
			s.EnableCSM = checked
		}); err != nil {
			slog.Error("Failed to update AWS settings", "error", err)
			return
		}
		slog.Info("Updated AWS setting: EnableCSM", "value", checked)
	})
	csmEnabledToggle.SetChecked(current.EnableCSM)

	csmInfo := infoFactory("About CSM", `Watches AWS API calls so SSO Zen notices the moment your session expires — even mid-command — and prompts you to re-login.

To do this, it adds a small block to `+"`~/.aws/config`"+`. The block is tagged with SSO-ZEN comments and removed when you turn this off or quit.`)

	return widget.NewCard(SettingsCardTitle, "", container.NewHBox(csmEnabledToggle, csmInfo))
}
