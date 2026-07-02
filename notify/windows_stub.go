//go:build !windows

package notify

// windowsService is a stub on non-Windows platforms. The Service
// constructor never instantiates it outside windows builds, but the
// type needs to exist so the field on Service compiles.
type windowsService struct{}

func newWindowsService(_ *Service) *windowsService { return nil }
func (w *windowsService) notify(_ Notification)    {}
