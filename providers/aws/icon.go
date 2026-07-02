package aws

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed aws.svg
var awsIcon []byte

var awsIconResource = fyne.NewStaticResource("aws", awsIcon)

// Icon returns the provider's icon, shown beside each of its session
// rows. Implements the optional IconProvider interface consumed by tray.
func (p *Provider) Icon() fyne.Resource {
	return awsIconResource
}
